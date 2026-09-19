// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

// Command gentypes generates the extension's Bicep type definitions from the
// Kubernetes OpenAPI swagger file (swagger/swagger.json) into gen/types.json
// and gen/index.json, which the extension binary embeds and serves to the
// Bicep IDE.
//
// For every catalogued kind whose definition exists in the swagger, the
// body type is a fully typed object: every property carries the swagger
// description (shown on hover in the IDE), the swagger type/constraints
// (string lengths, integer ranges, string enums, int-or-string unions,
// arrays, maps) and required flags. Required flags come from the swagger's
// own "required" arrays plus a curated minimum-required overlay
// (requiredOverlay) for fields the API server rejects without, which the
// generated swagger does not mark (Kubernetes' generated OpenAPI is
// sparse on "required"). The CRD-only kinds (Gateway API, CSI snapshots —
// CRDs the core swagger never carries) are typed from the committed CRD
// schemas in swagger/crd/ (scripts/update-gateway-api.sh and
// scripts/update-snapshotter.sh), merged alongside the swagger. Kinds
// missing from both get the permissive body type (additionalProperties:
// any), so they still compile.
//
// Usage (from src/):
//
//	go run ./cmd/gentypes [-swagger ../swagger/swagger.json] [-out gen] [-version x.y.z]
package main

import (
	"bicep-ext-k8smanifest/k8s"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Azure/bicep-types/src/bicep-types-go/factory"
	"github.com/Azure/bicep-types/src/bicep-types-go/types"
	"github.com/Azure/bicep-types/src/bicep-types-go/writers"
)

const (
	extensionName = "k8smanifest"
	typesFileName = "types.json"

	// allScopes is the writable-scope set for the resource types: every
	// local scope the Bicep language knows. Readable scopes are deliberately
	// left empty (types.ScopeTypeNone): the extension implements neither Get
	// nor Delete, so a type advertising readability would let `existing`
	// resources compile and then fail at runtime (BCP037 on the body, or an
	// Unimplemented Get). Empty readable scopes make Bicep reject
	// `existing` with an accurate diagnostic at compile time instead.
	allScopes = types.AllExceptExtension | types.ScopeTypeExtension
)

// Repeated property-name literals (required overlay + body transformation).
const (
	propAPIVersion = "apiVersion"
	propKind       = "kind"
	propMetadata   = "metadata"
	propName       = "name"
	propSelector   = "selector"
	propSpec       = "spec"
	propStatus     = "status"
	propTemplate   = "template"
)

// swaggerPrefix maps an API group ("" = core) to the definition-name prefix
// used by the swagger file. Most groups use the Go package path
// "io.k8s.api.<pkg>.<version>.<Kind>"; the apiserver components use their
// own package paths (see apiextensions / kube-aggregator).
var swaggerPrefix = map[string]string{
	"":                             "io.k8s.api.core",
	"apps":                         "io.k8s.api.apps",
	"cilium.io":                    "io.k8s.api.cilium.io",   // CRD schemas from swagger/crd/ (update-cluster-crds.sh)
	"example.com":                  "io.k8s.api.example.com", // examples fixture CRD (update-cluster-crds.sh)
	"batch":                        "io.k8s.api.batch",
	"networking.k8s.io":            "io.k8s.api.networking",
	"rbac.authorization.k8s.io":    "io.k8s.api.rbac",
	"autoscaling":                  "io.k8s.api.autoscaling",
	"policy":                       "io.k8s.api.policy",
	"storage.k8s.io":               "io.k8s.api.storage",
	"coordination.k8s.io":          "io.k8s.api.coordination",
	"scheduling.k8s.io":            "io.k8s.api.scheduling",
	"node.k8s.io":                  "io.k8s.api.node",
	"certificates.k8s.io":          "io.k8s.api.certificates",
	"apiextensions.k8s.io":         "io.k8s.apiextensions-apiserver.pkg.apis.apiextensions",
	"apiregistration.k8s.io":       "io.k8s.kube-aggregator.pkg.apis.apiregistration",
	"events.k8s.io":                "io.k8s.api.events",
	"flowcontrol.apiserver.k8s.io": "io.k8s.api.flowcontrol",
	// Gateway API kinds are CRDs the core swagger never carries; their
	// definitions come from the committed CRD schemas (merged in from
	// swagger/crd/, see mergeExtraDefinitions), which use the regular Go
	// package-path layout.
	"gateway.networking.k8s.io": "io.k8s.api.gateway.networking.k8s.io",
	"snapshot.storage.k8s.io":   "io.k8s.api.snapshot.storage.k8s.io",
}

// requiredOverlay adds "required" flags the API server enforces but the
// generated swagger does not mark (its "required" arrays are sparse).
// Keys are swagger definition names; values are property names. Entries for
// definitions that are absent from the given swagger are ignored. Keep this
// to the minimum mandatory set: over-requiring makes authoring annoying,
// under-requiring just defers validation to kubectl. A field is only listed
// here when the API *validation* rejects the resource without it — NOT when
// it is merely required for the workload to function (e.g. Toleration.effect
// defaults to "match all effects", HPA minReplicas defaults to 1, and an
// Ingress with only defaultBackend is valid; all were over-required before
// and rejected valid manifests at Bicep compile time).
var requiredOverlay = map[string][]string{
	// core/v1
	"io.k8s.api.core.v1.PodSpec":                   {"containers"},
	"io.k8s.api.core.v1.Container":                 {"image"},
	"io.k8s.api.core.v1.ContainerPort":             {"containerPort"},
	"io.k8s.api.core.v1.ServicePort":               {"port"},
	"io.k8s.api.core.v1.ServiceSpec":               {"ports"},
	"io.k8s.api.core.v1.PersistentVolumeSpec":      {"capacity"},
	"io.k8s.api.core.v1.PersistentVolumeClaimSpec": {"accessModes", "resources"},
	"io.k8s.api.core.v1.ReplicationControllerSpec": {propSelector, propTemplate},
	"io.k8s.api.core.v1.PodTemplate":               {propTemplate},
	"io.k8s.api.core.v1.LimitRangeSpec":            {"limits"},
	"io.k8s.api.core.v1.LocalObjectReference":      {propName},
	"io.k8s.api.core.v1.VolumeMount":               {propName, "mountPath"},
	// apps/v1
	"io.k8s.api.apps.v1.DeploymentSpec":  {propSelector, propTemplate},
	"io.k8s.api.apps.v1.ReplicaSetSpec":  {propSelector, propTemplate},
	"io.k8s.api.apps.v1.DaemonSetSpec":   {propSelector, propTemplate},
	"io.k8s.api.apps.v1.StatefulSetSpec": {"serviceName", propSelector, propTemplate},
	// batch/v1
	"io.k8s.api.batch.v1.JobSpec":     {propTemplate},
	"io.k8s.api.batch.v1.CronJobSpec": {"schedule", "jobTemplate"},
	// networking.k8s.io/v1
	"io.k8s.api.networking.v1.HTTPIngressRuleValue": {"paths"},
	"io.k8s.api.networking.v1.IngressClassSpec":     {"controller"},
	"io.k8s.api.networking.v1.NetworkPolicySpec":    {"podSelector"},
	// rbac.authorization.k8s.io/v1 (this swagger inlines the *Spec types, so
	// the mandatory sets live on the root kinds for Role/ClusterRole;
	// RoleRef/Subject are definitions). Subjects are deliberately NOT listed
	// (a binding without subjects is valid; verified by server-side dry-run
	// on 2026-09-14).
	"io.k8s.api.rbac.v1.Role":        {"rules"},
	"io.k8s.api.rbac.v1.ClusterRole": {"rules"},
	"io.k8s.api.rbac.v1.RoleRef":     {"apiGroup", propKind, propName},
	"io.k8s.api.rbac.v1.Subject":     {propKind, propName},
	// autoscaling/v2 (minReplicas is deliberately NOT listed: it defaults to
	// 1 and the swagger/validation do not require it)
	"io.k8s.api.autoscaling.v2.HorizontalPodAutoscalerSpec": {"scaleTargetRef"},
	"io.k8s.api.autoscaling.v2.CrossVersionObjectReference": {propKind, propName},
	// storage.k8s.io/v1
	"io.k8s.api.storage.v1.VolumeAttachmentSpec": {"attacher"},
	// certificates.k8s.io/v1
	"io.k8s.api.certificates.v1.CertificateSigningRequestSpec": {"request"},
	// apiextensions.k8s.io/v1 (swagger marks these already; kept for clarity)
	"io.k8s.apiextensions-apiserver.pkg.apis.apiextensions.v1.CustomResourceDefinitionSpec": {"group", "names", "scope", "versions"},
	// apiregistration.k8s.io/v1
	"io.k8s.kube-aggregator.pkg.apis.apiregistration.v1.APIServiceSpec":   {"group", "version", "service"},
	"io.k8s.kube-aggregator.pkg.apis.apiregistration.v1.ServiceReference": {propName, "namespace"},
	// events.k8s.io/v1 (message is "note" in this API group). note is
	// deliberately NOT listed (optional; reason and eventTime are the
	// validation-mandatory fields — verified by server-side dry-run on
	// 2026-09-14).
	"io.k8s.api.events.v1.Event": {"reason"},
	// flowcontrol (only v1beta3 exists in the provided swagger; the catalogue
	// declares v1, which falls back to the permissive body, so these entries
	// are inert unless the swagger or the catalogue changes)
	"io.k8s.api.flowcontrol.v1beta3.FlowSchemaSpec":                 {"distinguisherMethod", "matchingPrecedence", "priorityLevelConfiguration"},
	"io.k8s.api.flowcontrol.v1beta3.PriorityLevelConfigurationSpec": {"type"},
}

func main() {
	swaggerPath := flag.String("swagger", "../swagger/swagger.json", "path to the Kubernetes OpenAPI swagger.json")
	outDir := flag.String("out", "gen", "directory to write types.json and index.json into")
	extVersion := flag.String("version", "", "extension version stamped into index.json settings.version (required; scripts/build.sh passes $EXT_VERSION)")

	flag.Parse()

	if *extVersion == "" {
		fmt.Fprintln(os.Stderr, "gentypes: -version is required (e.g. -version 0.1.2); it must match the EXT_VERSION the scripts build with")
		os.Exit(1)
	}

	if err := run(*swaggerPath, *outDir, *extVersion); err != nil {
		fmt.Fprintln(os.Stderr, "gentypes: "+err.Error())
		os.Exit(1)
	}
}

// swaggerDefName returns the swagger definition name for a catalogued kind,
// or "" if the group is not known to the swagger layout.
func swaggerDefName(k k8s.Kind) string {
	prefix, ok := swaggerPrefix[k.Group]
	if !ok {
		return ""
	}

	return fmt.Sprintf("%s.%s.%s", prefix, k.Version, k.Kind)
}

// mergeExtraDefinitions merges the committed CRD schemas (swagger/crd/*.json,
// produced by scripts/update-gateway-api.sh and scripts/update-snapshotter.sh)
// into the core swagger's definitions, file by file in sorted order so the
// result is deterministic. A missing directory is not an error: the CRD
// kinds simply fall back to the permissive body, as before. A parse failure
// or a definition-name collision IS an error — the sources must never
// disagree about a shape.
func mergeExtraDefinitions(crdDir string, defs map[string]*schema) error {
	entries, err := os.ReadDir(crdDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil // no CRD schemas: those kinds stay permissive
	}

	if err != nil {
		return fmt.Errorf("reading %s: %w", crdDir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}

	slices.Sort(names)

	for _, name := range names {
		path := filepath.Join(crdDir, name)

		extraData, err := os.ReadFile(path) // #nosec G304 -- developer-supplied local path at build time (codegen tool)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		var extra struct {
			Definitions map[string]*schema `json:"definitions"`
		}
		if err := json.Unmarshal(extraData, &extra); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}

		for defName, def := range extra.Definitions {
			if _, dup := defs[defName]; dup {
				return fmt.Errorf("definition %s is present in both the swagger and %s", defName, path)
			}

			defs[defName] = def
		}

		fmt.Printf("gentypes: merged %d CRD definitions from %s\n", len(extra.Definitions), path)
	}

	return nil
}

func run(swaggerPath, outDir, extVersion string) error {
	data, err := os.ReadFile(swaggerPath) // #nosec G304 -- developer-supplied local path at build time (codegen tool)
	if err != nil {
		return fmt.Errorf("reading swagger: %w", err)
	}

	var sw struct {
		Definitions map[string]*schema `json:"definitions"`
	}
	if err := json.Unmarshal(data, &sw); err != nil {
		return fmt.Errorf("parsing swagger: %w", err)
	}

	// The CRD-only kinds (Gateway API, CSI snapshots) are absent from the
	// core swagger: their definitions come from the committed CRD schemas in
	// swagger/crd/ (produced by scripts/update-gateway-api.sh and
	// scripts/update-snapshotter.sh), merged in here. Missing directory =
	// those kinds fall back to the permissive body, as before. Any
	// definition-name collision is a hard error: the sources must never
	// disagree about a shape.
	if err := mergeExtraDefinitions(filepath.Join(filepath.Dir(swaggerPath), "crd"), sw.Definitions); err != nil {
		return err
	}

	g := newGenerator(sw.Definitions)

	// Build a resource type for every catalogued kind (typed body when the
	// swagger carries the definition, permissive otherwise).
	resourceRefs := map[string]types.ITypeReference{}

	var permissive []string

	for _, kind := range k8s.Kinds {
		bodyRef, typed := g.bodyFor(kind)
		if !typed {
			permissive = append(permissive, kind.BicepTypeReference())
		}

		resource := g.fac.CreateResourceType(kind.BicepTypeReference(), bodyRef, types.ScopeTypeNone, allScopes, nil)

		resourceRef, ok := g.fac.GetReference(resource).(types.TypeReference)
		if !ok {
			return fmt.Errorf("failed to take a type reference for %s", kind.BicepTypeReference())
		}

		// The index is keyed by "<type>@<version>", the same flat format the
		// Bicep CLI deserialises. Each entry is a CrossFileTypeReference so
		// the $ref carries the types.json relative path (a bare "#/N" makes
		// the CLI's packer fail with a misleading error).
		resourceRefs[kind.BicepTypeReference()] = types.CrossFileTypeReference{
			RelativePath: typesFileName,
			Ref:          resourceRef.Ref,
		}
	}

	fallbackRef, rawRef, configRef, err := g.handAuthoredTypes()
	if err != nil {
		return err
	}

	// The k8smanifest/Raw verbatim passthrough is a first-class extension
	// type (not a Kubernetes kind): hand-authored below, indexed like the
	// catalogued kinds so the compiler type-checks its body and no BCP081
	// is reported.
	resourceRefs[k8s.RawBicepType+"@v1"] = rawRef

	index := struct {
		Resources         map[string]types.ITypeReference `json:"resources"`
		ResourceFunctions map[string]any                  `json:"resourceFunctions"`
		FallbackResource  types.ITypeReference            `json:"fallbackResourceType"`
		Settings          indexSettings                   `json:"settings"`
	}{
		Resources:         resourceRefs,
		ResourceFunctions: map[string]any{},
		FallbackResource:  fallbackRef,
		Settings: indexSettings{
			Name:              extensionName,
			Version:           extVersion,
			ConfigurationType: configRef,
		},
	}

	typesJSON, err := writers.NewJSONWriter().WriteTypesToString(g.fac.GetTypes())
	if err != nil {
		return fmt.Errorf("serializing types: %w", err)
	}

	indexJSON, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("serializing index: %w", err)
	}

	detTypes, detIndex, err := deterministicOutput(typesJSON, string(indexJSON))
	if err != nil {
		return fmt.Errorf("determinizing output: %w", err)
	}

	typesJSON = detTypes
	indexJSON = []byte(detIndex)

	if err := writeGenFiles(outDir, map[string]string{
		"types.json": typesJSON,
		"index.json": string(indexJSON),
	}); err != nil {
		return err
	}

	sort.Strings(permissive)
	fmt.Printf("gentypes: %d types, %d swagger definitions used\n", len(g.fac.GetTypes()), len(g.defsUsed))
	fmt.Printf("gentypes: %d catalogued kinds typed from the swagger; %d fell back to the permissive body type: %v\n",
		len(k8s.Kinds)-len(permissive), len(permissive), permissive)

	for _, w := range g.warnings {
		fmt.Printf("gentypes: warning: %s\n", w)
	}

	fmt.Printf("gentypes: wrote %s/types.json (%d bytes) and %s/index.json\n",
		outDir, len(typesJSON), outDir)

	return nil
}

// bareRefPattern matches a same-file $ref value exactly as it appears inside
// the writer's JSON (Go's MarshalIndent layout: `"$ref": "#/N"`), capturing
// the position. Scoping the match to the $ref key (rather than any string
// value that equals "#/N") means a description or string-literal type value
// that happens to be "#/5" is never corrupted. The layout dependency (Go's
// encoder always emits `: ` separators) is the same one the entry re-emission
// already relies on.
var bareRefPattern = regexp.MustCompile(`"\$ref": "#/(\d+)"`)

// crossRefPattern matches a cross-file $ref value into types.json
// ("types.json#/N") the same way bareRefPattern matches same-file references.
var crossRefPattern = regexp.MustCompile(`"\$ref": "types\.json#/(\d+)"`)

// canonRefPattern matches a position reference value ("#/N") when walking
// parsed entries for the sort key.
var canonRefPattern = regexp.MustCompile(`^#/(\d+)$`)

// writeGenFiles writes the generated files into outDir.
func writeGenFiles(outDir string, files map[string]string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil { // #nosec G301 -- committed generated source, needs normal read access
		return err
	}

	for file, content := range files {
		if err := os.WriteFile(filepath.Join(outDir, file), []byte(content), 0o644); err != nil { // #nosec G306 -- committed generated source, needs normal read access
			return err
		}
	}

	return nil
}

// deterministicOutput makes the generated files byte-stable across runs.
// The factory writer emits the type entries in its internal map's iteration
// order, which changes every run, and every $ref in both files is a
// position into that array. The function (1) merges content-identical
// entries (the factory does not intern anonymous types, so repeated
// primitive/array types appear as separate twins) into a single entry,
// (2) re-sorts the surviving entries by a stable key (name, then $type,
// then the entry's ref-resolved content — unique after the merge) and
// (3) rewrites all position references to follow the reordering and the
// merges. The entry layout is the writer's: two-space indented array,
// entries separated by ",\n". Regenerating with unchanged inputs must
// produce byte-identical files so the committed gen/ artifacts do not flap
// in git.
func deterministicOutput(typesJSON, indexJSON string) (string, string, error) {
	raws, parsed, err := parseTypeEntries(typesJSON)
	if err != nil {
		return "", "", fmt.Errorf("parsing types output: %w", err)
	}

	canon, mergeTo := mergeTwins(parsed)

	keys := sortedRepresentatives(raws, parsed, canon, mergeTo)

	ordered, newPos := assignPositions(keys, mergeTo)

	rewritten := make([]string, len(ordered))
	for i, raw := range ordered {
		rewritten[i] = remapBareRefs(raw, newPos)
	}

	out := "[\n  " + strings.Join(rewritten, ",\n  ") + "\n]"

	return out, remapCrossRefs(indexJSON, newPos), nil
}

// parseTypeEntries parses the writer's types array into raw entries (for
// byte-preserving re-emission) and parsed entries (for content analysis).
func parseTypeEntries(typesJSON string) ([]json.RawMessage, []map[string]any, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal([]byte(typesJSON), &raws); err != nil {
		return nil, nil, err
	}

	parsed := make([]map[string]any, len(raws))
	for i, r := range raws {
		if err := json.Unmarshal(r, &parsed[i]); err != nil {
			return nil, nil, fmt.Errorf("entry %d: %w", i, err)
		}
	}

	return raws, parsed, nil
}

// mergeTwins computes each entry's ref-expanded canonical content (stable
// across writer orders) and groups content-identical entries ("twins",
// which occur because the factory does not intern anonymous types). It
// returns the canonical content per entry and mergeTo, which maps every old
// position to its group's representative (first in writer order).
func mergeTwins(parsed []map[string]any) (canon []string, mergeTo []int) {
	canon = make([]string, len(parsed))
	for i := range parsed {
		canon[i] = canonicalValue(parsed[i], parsed, map[int]bool{})
	}

	mergeTo = make([]int, len(parsed))

	seen := make(map[string]int, len(parsed))
	for i := range parsed {
		if rep, ok := seen[canon[i]]; ok {
			mergeTo[i] = rep

			continue
		}

		seen[canon[i]] = i
		mergeTo[i] = i
	}

	return canon, mergeTo
}

// sortKey identifies one surviving entry and its new position.
type sortKey struct {
	name string
	t    string
	key  string
	raw  json.RawMessage
	old  int
}

// sortedRepresentatives returns the surviving entries (one per content
// group) in stable order: name, then $type, then ref-resolved content.
func sortedRepresentatives(raws []json.RawMessage, parsed []map[string]any, canon []string, mergeTo []int) []sortKey {
	keys := make([]sortKey, 0, len(raws))
	for i := range raws {
		if mergeTo[i] != i {
			continue
		}

		name, _ := parsed[i]["name"].(string)
		t, _ := parsed[i]["$type"].(string)
		keys = append(keys, sortKey{name, t, canon[i], raws[i], i})
	}

	sort.Slice(keys, func(a, b int) bool { return lessSortKey(keys[a], keys[b]) })

	return keys
}

// lessSortKey orders two representatives; the key is unique after the
// content merge, so the order is deterministic.
func lessSortKey(ka, kb sortKey) bool {
	if ka.name != kb.name {
		return ka.name < kb.name
	}

	if ka.t != kb.t {
		return ka.t < kb.t
	}

	return ka.key < kb.key
}

// assignPositions returns the ordered raw entries and newPos, which maps
// every old (writer-order) position to its new position; merged twins all
// resolve to their representative's position.
func assignPositions(keys []sortKey, mergeTo []int) ([]json.RawMessage, []int) {
	newPos := make([]int, len(mergeTo))
	ordered := make([]json.RawMessage, 0, len(keys))

	for newIdx, k := range keys {
		ordered = append(ordered, k.raw)

		for old := range mergeTo {
			if mergeTo[old] == k.old {
				newPos[old] = newIdx
			}
		}
	}

	return ordered, newPos
}

// remapBareRefs rewrites every `"$ref": "#/N"` in a raw entry with the new
// position. Out-of-range references are left untouched rather than
// silently re-pointed.
func remapBareRefs(raw json.RawMessage, newPos []int) string {
	return string(bareRefPattern.ReplaceAllFunc(raw, func(m []byte) []byte {
		n, err := refPosition(m)
		if err != nil || n < 0 || n >= len(newPos) {
			return m
		}

		return fmt.Appendf(nil, `"$ref": "#/%d"`, newPos[n])
	}))
}

// remapCrossRefs rewrites every `"$ref": "types.json#/N"` in the index file
// the same way remapBareRefs rewrites same-file references.
func remapCrossRefs(indexJSON string, newPos []int) string {
	return string(crossRefPattern.ReplaceAllFunc([]byte(indexJSON), func(m []byte) []byte {
		n, err := refPosition(m)
		if err != nil || n < 0 || n >= len(newPos) {
			return m
		}

		return fmt.Appendf(nil, `"$ref": "types.json#/%d"`, newPos[n])
	}))
}

// refPosition extracts the position N from a match of bareRefPattern or
// crossRefPattern: the digits sit between `#/` and the closing quote.
func refPosition(m []byte) (int, error) {
	i := bytes.IndexByte(m, '#')
	if i < 0 {
		return 0, errors.New("no position marker")
	}

	return strconv.Atoi(string(m[i+2 : len(m)-1]))
}

// canonicalValue renders a parsed entry as a deterministic string: object
// keys sorted, and every position reference ("#/N") expanded to the
// canonical form of the entry it points at. Position-independent, so it is
// a stable content key even for anonymous entries whose only content is a
// ref.
func canonicalValue(v any, entries []map[string]any, visiting map[int]bool) string {
	switch t := v.(type) {
	case map[string]any:
		return canonObject(t, entries, visiting)
	case []any:
		return canonSlice(t, entries, visiting)
	case string:
		return canonString(t, entries, visiting)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// canonObject renders an object with its keys sorted.
func canonObject(m map[string]any, entries []map[string]any, visiting map[int]bool) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}

	sort.Strings(ks)

	b := strings.Builder{}
	b.WriteByte('{')

	for i, k := range ks {
		if i > 0 {
			b.WriteByte(',')
		}

		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(canonicalValue(m[k], entries, visiting))
	}

	b.WriteByte('}')

	return b.String()
}

// canonSlice renders an array in element order.
func canonSlice(s []any, entries []map[string]any, visiting map[int]bool) string {
	b := strings.Builder{}
	b.WriteByte('[')

	for i, item := range s {
		if i > 0 {
			b.WriteByte(',')
		}

		b.WriteString(canonicalValue(item, entries, visiting))
	}

	b.WriteByte(']')

	return b.String()
}

// canonString expands a position reference ("#/N") into the canonical form
// of the entry it points at; non-reference strings are quoted verbatim.
// visiting guards against ref cycles (recursive types), which resolve to a
// marker.
func canonString(s string, entries []map[string]any, visiting map[int]bool) string {
	m := canonRefPattern.FindStringSubmatch(s)
	if m == nil {
		return strconv.Quote(s)
	}

	n, err := strconv.Atoi(m[1])
	if err != nil || n < 0 || n >= len(entries) {
		return s
	}

	if visiting[n] {
		return "<cycle>"
	}

	visiting[n] = true
	defer delete(visiting, n)

	return "<r:" + canonicalValue(entries[n], entries, visiting) + ">"
}

// schema is the subset of the JSON Schema (OpenAPI swagger 2.0) used by the
// Kubernetes definitions.
type schema struct {
	Ref                  string             `json:"$ref"`
	Type                 any                `json:"type"`
	Description          string             `json:"description"`
	Format               string             `json:"format"`
	Properties           map[string]*schema `json:"properties"`
	Required             []string           `json:"required"`
	Items                *schema            `json:"items"`
	AdditionalProperties *schema            `json:"additionalProperties"`
	Enum                 []any              `json:"enum"`
	MinLength            *int64             `json:"minLength"`
	MaxLength            *int64             `json:"maxLength"`
	Pattern              string             `json:"pattern"`
	Minimum              *float64           `json:"minimum"`
	Maximum              *float64           `json:"maximum"`
	AnyOf                []*schema          `json:"anyOf"`
	OneOf                []*schema          `json:"oneOf"`
	XPreserveUnknown     bool               `json:"x-kubernetes-preserve-unknown-fields"`
	XIntOrString         bool               `json:"x-kubernetes-int-or-string"`
}

// firstType returns the primary JSON schema type; the "type" field may be a
// string or (per JSON Schema) an array of strings.
func (s *schema) firstType() string {
	switch t := s.Type.(type) {
	case string:
		return t
	case []any:
		if len(t) > 0 {
			if str, ok := t[0].(string); ok {
				return str
			}
		}
	}

	return ""
}

// generator converts swagger definitions into Bicep types.
type generator struct {
	fac       *factory.TypeFactory
	defs      map[string]*schema
	defsUsed  map[string]types.ITypeReference // definition name -> type reference
	anonRef   map[string]types.ITypeReference // anonymous object name -> type reference
	warnings  []string
	plainStr  types.ITypeReference // cached plain primitives (dedupes the type file)
	plainInt  types.ITypeReference
	plainBool types.ITypeReference
}

func newGenerator(defs map[string]*schema) *generator {
	return &generator{
		fac:      factory.NewTypeFactory(),
		defs:     defs,
		defsUsed: map[string]types.ITypeReference{},
		anonRef:  map[string]types.ITypeReference{},
		warnings: []string{},
	}
}

// stringRef / integerRef / boolRef return single shared references to the
// plain primitive types (the factory does not dedupe primitives, so caching
// here keeps the generated type file small).
func (g *generator) stringRef() types.ITypeReference {
	if g.plainStr == nil {
		g.plainStr = g.fac.GetReference(g.fac.CreateStringType())
	}

	return g.plainStr
}

func (g *generator) integerRef() types.ITypeReference {
	if g.plainInt == nil {
		g.plainInt = g.fac.GetReference(g.fac.CreateIntegerType())
	}

	return g.plainInt
}

func (g *generator) boolRef() types.ITypeReference {
	if g.plainBool == nil {
		g.plainBool = g.fac.GetReference(g.fac.CreateBooleanType())
	}

	return g.plainBool
}

func (g *generator) warn(format string, args ...any) {
	g.warnings = append(g.warnings, fmt.Sprintf(format, args...))
}

// anyRef is the permissive "any" reference (created once).
func (g *generator) anyRef() types.ITypeReference {
	// AnyType instances are distinct pointers; cache via a dedicated helper.
	if ref, ok := g.anonRef["<any>"]; ok {
		return ref
	}

	ref := g.fac.GetReference(g.fac.CreateAnyType())
	g.anonRef["<any>"] = ref

	return ref
}

// unionOf dedupes a set of references (by pointer identity) and returns the
// single type if only one remains, else a union type.
func (g *generator) unionOf(refs ...types.ITypeReference) types.ITypeReference {
	// First occurrence wins. Track indices, not values: equal values (the
	// common case — the same reference passed twice) compare equal to the
	// stored one, so a value comparison can't tell the first occurrence from
	// its duplicates.
	firstAt := map[string]int{}
	for i, r := range refs {
		if _, ok := firstAt[refID(r)]; !ok {
			firstAt[refID(r)] = i
		}
	}

	deduped := make([]types.ITypeReference, 0, len(firstAt))
	for i, r := range refs {
		if firstAt[refID(r)] == i {
			deduped = append(deduped, r)
		}
	}

	if len(deduped) == 1 {
		return deduped[0]
	}

	return g.fac.GetReference(g.fac.CreateUnionType(deduped))
}

// refID gives a stable identity key for a reference (factory references
// wrap the type pointer; the writer only cares about equality).
func refID(r types.ITypeReference) string {
	v := reflect.ValueOf(r)
	if v.Kind() == reflect.Pointer {
		return fmt.Sprintf("ptr:%x", v.Pointer())
	}

	return fmt.Sprintf("val:%v", v.Interface())
}

// requiredFor returns the set of required property names for a named
// definition: the swagger's own "required" array plus the curated overlay.
func (g *generator) requiredFor(defName string, s *schema) map[string]bool {
	required := map[string]bool{}
	for _, name := range s.Required {
		required[name] = true
	}

	for _, name := range requiredOverlay[defName] {
		if _, ok := s.Properties[name]; !ok {
			g.warn("required overlay property %q not present on %s — skipped", name, defName)

			continue
		}

		required[name] = true
	}

	return required
}

// defRef returns the type reference for a full swagger definition, creating
// (and memoising) its type on first use.
//
// Object definitions are registered with the factory BEFORE their properties
// are converted, so recursive $ref graphs (e.g. JSONSchemaProps.items ->
// JSONSchemaPropsOrArray.items -> JSONSchemaProps) resolve to the type
// being built instead of recursing; the object is populated in place
// (the writer serialises everything only at the end).
func (g *generator) defRef(defName string) types.ITypeReference {
	if ref, ok := g.defsUsed[defName]; ok {
		return ref
	}

	s, ok := g.defs[defName]
	if !ok {
		g.warn("swagger definition %s referenced but absent — using any", defName)
		g.defsUsed[defName] = g.anyRef()

		return g.anyRef()
	}

	// Non-object definitions (e.g. IntOrString: format "int-or-string")
	// are created directly.
	if s.Format == "int-or-string" || s.XIntOrString {
		ref := g.unionOf(g.stringRef(), g.integerRef())
		g.defsUsed[defName] = ref

		return ref
	}

	if ref, handled := g.scalarDefRef(defName, s); handled {
		return ref
	}

	if s.firstType() == "array" {
		// Latent: top-level array definitions do not occur in the bundled
		// swagger; route them through the item-converting path instead of
		// falling through to an object type.
		ref := g.convertArray(s, defName)
		g.defsUsed[defName] = ref

		return ref
	}

	obj := g.fac.CreateObjectType(defName, map[string]types.ObjectTypeProperty{}, nil, nil)
	ref := g.fac.GetReference(obj)
	g.defsUsed[defName] = ref

	obj.Properties = g.objectProps(defName, s, s.Properties)
	if addl := g.additionalPropsRef(defName, s); addl != nil {
		obj.AdditionalProperties = addl
	}

	return ref
}

// scalarDefRef handles the scalar definition shapes (string, integer,
// boolean, number); (nil, false) is returned for anything else so the
// caller falls through to the object path. Every branch memoises into
// defsUsed, so the warning for a number definition fires once per name.
func (g *generator) scalarDefRef(defName string, s *schema) (types.ITypeReference, bool) {
	switch s.firstType() {
	case "string":
		if s.MinLength == nil && s.MaxLength == nil && s.Pattern == "" && !isSensitive(defName) {
			g.defsUsed[defName] = g.stringRef()

			return g.stringRef(), true
		}

		ref := g.fac.GetReference(g.fac.CreateStringTypeWithConstraints(
			s.MinLength, s.MaxLength, s.Pattern, isSensitive(defName),
		))
		g.defsUsed[defName] = ref

		return ref, true
	case "integer":
		ref := g.fac.GetReference(g.fac.CreateIntegerTypeWithConstraints(
			int64Ptr(s.Minimum), int64Ptr(s.Maximum),
		))
		g.defsUsed[defName] = ref

		return ref, true
	case "boolean":
		g.defsUsed[defName] = g.boolRef()

		return g.boolRef(), true
	case "number":
		// bicep-types-go has no float type; be permissive rather than emit
		// a bogus object type. Latent: the bundled swagger has no
		// type:"number" definitions.
		g.warn("swagger definition %s has type \"number\"; no float type in bicep-types-go — using any", defName)
		g.defsUsed[defName] = g.anyRef()

		return g.anyRef(), true
	}

	return nil, false
}

// objectProps converts the named properties of a definition into Bicep
// object properties (typed, described, required-flagged).
func (g *generator) objectProps(defName string, s *schema, props map[string]*schema) map[string]types.ObjectTypeProperty {
	required := g.requiredFor(defName, s)

	out := make(map[string]types.ObjectTypeProperty, len(props))
	for name, p := range props {
		prop := g.property(defName+"."+name, p, required[name])
		out[name] = prop
	}

	return out
}

// property converts one property schema into a Bicep object property.
func (g *generator) property(nameHint string, s *schema, required bool) types.ObjectTypeProperty {
	flags := types.TypePropertyFlagsNone
	if required {
		flags = types.TypePropertyFlagsRequired
	}

	return types.ObjectTypeProperty{
		Type:        g.convert(s, nameHint),
		Flags:       flags,
		Description: s.Description,
	}
}

// additionalPropsRef returns the additionalProperties reference for an
// object schema (a map type), or nil.
func (g *generator) additionalPropsRef(defName string, s *schema) types.ITypeReference {
	if s.AdditionalProperties != nil {
		return g.convert(s.AdditionalProperties, defName+".<value>")
	}

	if s.XPreserveUnknown {
		return g.anyRef()
	}

	return nil
}

// convert converts a schema node into a Bicep type reference. nameHint is a
// deterministic, human-readable name used for anonymous object types.
func (g *generator) convert(s *schema, nameHint string) types.ITypeReference {
	if s == nil {
		return g.anyRef()
	}

	if s.Ref != "" {
		return g.defRef(strings.TrimPrefix(s.Ref, "#/definitions/"))
	}

	// IntOrString (and friends): the swagger marks these via the
	// "int-or-string" format or the x-kubernetes-int-or-string extension.
	if s.Format == "int-or-string" || s.XIntOrString {
		return g.intOrStringRef()
	}

	switch s.firstType() {
	case "object":
		return g.convertObject(s, nameHint)
	case "array":
		return g.convertArray(s, nameHint)
	case "string":
		return g.convertString(s, nameHint)
	case "integer":
		return g.fac.GetReference(g.fac.CreateIntegerTypeWithConstraints(
			int64Ptr(s.Minimum), int64Ptr(s.Maximum),
		))
	case "boolean":
		return g.fac.GetReference(g.fac.CreateBooleanType())
	}

	// No "type" (or an unrecognised one): try composite keywords, else any.
	if len(s.AnyOf) > 0 || len(s.OneOf) > 0 {
		return g.convertComposite(s, nameHint)
	}

	return g.anyRef()
}

// intOrStringRef is the string | int union used for IntOrString fields.
func (g *generator) intOrStringRef() types.ITypeReference {
	return g.unionOf(g.stringRef(), g.integerRef())
}

// convertObject converts an object schema: a named property object, a map
// (additionalProperties) or a permissive object (preserve-unknown-fields).
func (g *generator) convertObject(s *schema, nameHint string) types.ITypeReference {
	if len(s.Properties) > 0 || s.AdditionalProperties != nil || s.XPreserveUnknown {
		return g.anonObject(nameHint, s)
	}

	return g.anyRef()
}

// convertArray converts an array schema, honouring length constraints when
// present (the swagger usually omits them).
func (g *generator) convertArray(s *schema, nameHint string) types.ITypeReference {
	item := g.anyRef()
	if s.Items != nil {
		item = g.convert(s.Items, nameHint+"[]")
	}

	arr := g.fac.CreateArrayType(item)
	if s.MinLength != nil || s.MaxLength != nil {
		arr = g.fac.CreateArrayTypeWithConstraints(item, s.MinLength, s.MaxLength)
	}

	return g.fac.GetReference(arr)
}

// convertString converts a string schema: string enums become a union of
// string literals (IDE autocomplete), otherwise a string type with the
// swagger constraints (length/pattern/sensitive).
func (g *generator) convertString(s *schema, nameHint string) types.ITypeReference {
	if len(s.Enum) > 0 {
		var lits []types.ITypeReference

		for _, v := range s.Enum {
			if str, ok := v.(string); ok {
				lits = append(lits, g.fac.GetReference(g.fac.CreateStringLiteralType(str)))
			}
		}

		if len(lits) > 0 {
			return g.unionOf(lits...)
		}
	}

	if s.MinLength == nil && s.MaxLength == nil && s.Pattern == "" && !isSensitive(nameHint) {
		return g.stringRef()
	}

	return g.fac.GetReference(g.fac.CreateStringTypeWithConstraints(
		s.MinLength, s.MaxLength, s.Pattern, isSensitive(nameHint),
	))
}

// convertComposite converts anyOf/oneOf schemas to a union of their members.
func (g *generator) convertComposite(s *schema, nameHint string) types.ITypeReference {
	var members []types.ITypeReference
	for _, sub := range append(append([]*schema{}, s.AnyOf...), s.OneOf...) {
		members = append(members, g.convert(sub, nameHint))
	}

	return g.unionOf(members...)
}

// anonObject returns (creating and memoising) the object type for an
// anonymous (non-$ref) object schema.
func (g *generator) anonObject(nameHint string, s *schema) types.ITypeReference {
	if ref, ok := g.anonRef[nameHint]; ok {
		return ref
	}

	props := make(map[string]types.ObjectTypeProperty, len(s.Properties))
	for name, p := range s.Properties {
		props[name] = g.property(nameHint+"."+name, p, false)
	}

	addl := g.additionalPropsRef(nameHint, s)
	obj := g.fac.CreateObjectType(nameHint, props, addl, nil)
	ref := g.fac.GetReference(obj)
	g.anonRef[nameHint] = ref

	return ref
}

// isSensitive marks private-key-style string fields so the IDE redacts them.
func isSensitive(nameHint string) bool {
	lower := strings.ToLower(nameHint)

	return strings.Contains(lower, "privatekey") || strings.Contains(lower, "clientkey")
}

func int64Ptr(f *float64) *int64 {
	if f == nil {
		return nil
	}

	v := int64(*f)

	return &v
}

// responseProps are the read-only properties the extension adds to the
// resource properties after CreateOrUpdate (the Bicep body type doubles as
// the response type, so samples can reference e.g. deployment.filePath).
func (g *generator) responseProps() map[string]types.ObjectTypeProperty {
	readOnly := types.TypePropertyFlagsReadOnly

	return map[string]types.ObjectTypeProperty{
		"filePath": {
			Type:        g.stringRef(),
			Flags:       readOnly,
			Description: "Path of the generated YAML manifest file, relative to the bicep CLI working directory.",
		},
		"content": {
			Type:        g.stringRef(),
			Flags:       readOnly,
			Description: "The rendered YAML content of the manifest.",
		},
		propAPIVersion: {
			Type:        g.stringRef(),
			Flags:       readOnly,
			Description: "The Kubernetes apiVersion resolved for this resource (added by the extension, never taken from the body).",
		},
		propKind: {
			Type:        g.stringRef(),
			Flags:       readOnly,
			Description: "The Kubernetes kind resolved for this resource (added by the extension, never taken from the body).",
		},
	}
}

// optionalSpec are kinds whose spec object the API does not require (the
// "spec is required when the kind has one" rule would otherwise force it).
var optionalSpec = map[string]bool{
	"Namespace": true,
	"Endpoints": true,
	// The CiliumNetworkPolicy CRD's openAPIV3Schema only requires metadata
	// (an empty spec is a valid policy object); the "spec is required when
	// the kind has one" rule would otherwise over-constrain the type.
	"CiliumNetworkPolicy": true,
}

// bodyRequired computes the mandatory-minimum required set for a typed
// body: metadata (always) and spec (when the kind has one and the API
// requires it), plus any swagger/overlay-required root properties (e.g.
// StorageClass.provisioner, PriorityClass.value).
func bodyRequired(k k8s.Kind, defName string, s *schema, props map[string]types.ObjectTypeProperty) map[string]bool {
	required := map[string]bool{propMetadata: true}

	if !optionalSpec[k.Kind] {
		if _, ok := props[propSpec]; ok {
			required[propSpec] = true
		}
	}

	for _, name := range s.Required {
		if _, ok := props[name]; ok {
			required[name] = true
		}
	}

	for _, name := range requiredOverlay[defName] {
		if _, ok := props[name]; ok {
			required[name] = true
		}
	}

	return required
}

// bodyFor creates the resource body type for a catalogued kind. It returns
// (bodyRef, typed); typed is false when the swagger lacks the definition and
// the permissive body was used instead.
func (g *generator) bodyFor(k k8s.Kind) (types.ITypeReference, bool) {
	defName := swaggerDefName(k)

	s, ok := g.defs[defName]
	if !ok {
		return g.permissiveBody(k), false
	}

	// The body is the manifest minus the envelope: apiVersion and kind are
	// added by the extension (never taken from the body) and status is
	// server-managed.
	props := make(map[string]types.ObjectTypeProperty)

	for name, p := range s.Properties {
		switch name {
		case propAPIVersion, propKind, propStatus:
			continue
		}

		props[name] = g.property(defName+"."+name, p, false)
	}

	required := bodyRequired(k, defName, s, props)

	for name, p := range props {
		if required[name] {
			p.Flags = types.TypePropertyFlagsRequired
			props[name] = p
		}
	}

	// The read-only response properties (filePath, content, apiVersion,
	// kind) are part of the body type too: Bicep uses the body type for
	// both the input body and the properties returned after CreateOrUpdate.
	for name, p := range g.responseProps() {
		if _, ok := props[name]; !ok {
			props[name] = p
		}
	}

	body := defName + "Body"
	obj := g.fac.CreateObjectType(body, props, nil, nil)

	return g.fac.GetReference(obj), true
}

// permissiveBody is the body type for catalogued kinds the swagger does not
// cover: metadata.name and metadata.generateName are both optional (the
// runtime requires one or the other, or neither for the nameless kinds),
// and metadata/spec accept anything else (any). The body itself also
// accepts additional properties.
func (g *generator) permissiveBody(k k8s.Kind) types.ITypeReference {
	name := k.BicepTypeName() + "Body"
	if ref, ok := g.anonRef[name]; ok {
		return ref
	}

	metadata := g.fac.GetReference(g.fac.CreateObjectType(
		name+".metadata", map[string]types.ObjectTypeProperty{
			propName: {
				Type:        g.fac.GetReference(g.fac.CreateStringType()),
				Flags:       types.TypePropertyFlagsNone,
				Description: "Name of the resource.",
			},
			"generateName": {
				Type:        g.fac.GetReference(g.fac.CreateStringType()),
				Flags:       types.TypePropertyFlagsNone,
				Description: "Prefix of the name assigned by the server at apply time; an alternative to name on the kinds that support it.",
			},
		}, g.anyRef(), nil,
	))
	spec := g.fac.GetReference(g.fac.CreateObjectType(
		name+".spec", map[string]types.ObjectTypeProperty{}, g.anyRef(), nil,
	))

	bodyProps := map[string]types.ObjectTypeProperty{
		propMetadata: {
			Type:        metadata,
			Flags:       types.TypePropertyFlagsRequired,
			Description: "Standard object metadata (name, namespace, labels, annotations, ...).",
		},
		propSpec: {
			Type:        spec,
			Flags:       types.TypePropertyFlagsNone,
			Description: "Specified desired state of the resource.",
		},
	}
	maps.Copy(bodyProps, g.responseProps())

	obj := g.fac.CreateObjectType(name, bodyProps, g.anyRef(), nil)
	ref := g.fac.GetReference(obj)
	g.anonRef[name] = ref

	return ref
}

// handAuthoredTypes builds the fallback resource type (for un-catalogued
// kinds such as CRDs), the k8smanifest/Raw verbatim passthrough type and
// the extension configuration type, all as cross-file references into
// types.json.
func (g *generator) handAuthoredTypes() (fallback, raw, config types.ITypeReference, err error) {
	// k8smanifest/Raw: the body is NOT a manifest body but the strict wrapper
	// { document: <manifest> } — Bicep resource bodies must be object
	// literals, so a loadYamlContent'd manifest enters through the single
	// document property (any type: the wrapper keeps it opaque). Closed
	// object (nil additional properties): a stray property beside document
	// is a compile error, matching the runtime's strict unwrapping.
	rawBody := g.fac.CreateObjectType("K8sManifestRawBody", map[string]types.ObjectTypeProperty{
		"document": {
			Type:        g.fac.GetReference(g.fac.CreateAnyType()),
			Flags:       types.TypePropertyFlagsNone,
			Description: "The complete manifest document to emit verbatim (it must carry its own apiVersion and kind — e.g. loadYamlContent of a SOPS-encrypted manifest).",
		},
	}, nil, nil)

	// Writable in the local scope exactly like the catalogued kinds and the
	// fallback (see the fallback construction below for the scope subtlety).
	rawObj := g.fac.CreateResourceType(k8s.RawBicepType+"@v1", g.fac.GetReference(rawBody),
		types.ScopeTypeNone, allScopes, nil)

	rawRef, ok := g.fac.GetReference(rawObj).(types.TypeReference)
	if !ok {
		return nil, nil, nil, errors.New("failed to take a type reference for the Raw type")
	}

	fallbackBodyProps := map[string]types.ObjectTypeProperty{
		propMetadata: {
			Type:        g.fac.GetReference(g.fac.CreateAnyType()),
			Flags:       types.TypePropertyFlagsNone,
			Description: "Standard object metadata (name, namespace, labels, annotations, ...).",
		},
		propSpec: {
			Type:        g.fac.GetReference(g.fac.CreateAnyType()),
			Flags:       types.TypePropertyFlagsNone,
			Description: "Specified desired state of the resource.",
		},
	}
	maps.Copy(fallbackBodyProps, g.responseProps())

	body := g.fac.CreateObjectType("AnyKubernetesManifestBody", fallbackBodyProps, g.anyRef(), nil)

	// The fallback type is writable-but-not-readable, exactly like the
	// catalogued kinds (readableScopes 0 makes `existing` CRD resources fail
	// at compile time with an accurate diagnostic rather than BCP037 on the
	// body). Writable scopes use the allScopes set — including the local
	// scope — so un-catalogued kinds (CR instances) compile under
	// `targetScope = 'local'` just like catalogued ones. (The factory's
	// unscoped variant only grants AllExceptExtension, which omits local.)
	fallbackObj := g.fac.CreateResourceType("AnyKubernetesManifest", g.fac.GetReference(body),
		types.ScopeTypeNone, allScopes, nil)

	fallbackRef, ok := g.fac.GetReference(fallbackObj).(types.TypeReference)
	if !ok {
		return nil, nil, nil, errors.New("failed to take a type reference for the fallback type")
	}

	stringRef := g.fac.GetReference(g.fac.CreateStringType())
	boolRef := g.fac.GetReference(g.fac.CreateBooleanType())

	configObj := g.fac.CreateObjectType("KubernetesExtensionConfig", map[string]types.ObjectTypeProperty{
		"outputDir": {
			Type:        stringRef,
			Flags:       types.TypePropertyFlagsNone,
			Description: "Directory (relative to the bicep CLI working directory) that generated YAML manifest files are written to. Defaults to 'manifests'.",
		},
		"combine": {
			Type:        boolRef,
			Flags:       types.TypePropertyFlagsNone,
			Description: "When true, all manifests are appended to a single '<outputDir>/combined.yaml' file ('---' separated) instead of one file per resource.",
		},
	}, nil, nil)

	configRef := g.fac.GetReference(configObj)

	configTypeRef, ok := configRef.(types.TypeReference)
	if !ok {
		return nil, nil, nil, errors.New("failed to take a type reference for the extension configuration type")
	}

	config = types.CrossFileTypeReference{RelativePath: typesFileName, Ref: configTypeRef.Ref}

	return types.CrossFileTypeReference{RelativePath: typesFileName, Ref: fallbackRef.Ref},
		types.CrossFileTypeReference{RelativePath: typesFileName, Ref: rawRef.Ref}, config, nil
}

type indexSettings struct {
	Name              string               `json:"name"`
	Version           string               `json:"version"`
	ConfigurationType types.ITypeReference `json:"configurationType,omitempty"`
}
