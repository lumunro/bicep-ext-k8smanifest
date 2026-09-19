// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

package main

import (
	"bicep-ext-k8smanifest/k8s"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// extensionConfig is the user-supplied `extension k8smanifest with { ... }` block.
type extensionConfig struct {
	OutputDir string `json:"outputDir"`
	Combine   bool   `json:"combine"`
}

const (
	defaultOutputDir = "manifests"
	combinedFileName = "combined.yaml"
	// namelessDocName is the metadata.name placeholder for the lenient
	// no-name kinds (Event, Binding, Status).
	namelessDocName = "unnamed"
)

// outputDir returns the effective output directory for a config, normalised
// (filepath.Clean) so that different spellings of the same directory
// ("out", "./out", "out/") map to one combined-file buffer and one file.
func outputDir(cfg *extensionConfig) string {
	if cfg.OutputDir == "" {
		return defaultOutputDir
	}

	return filepath.Clean(cfg.OutputDir)
}

// combinedMu guards the combinedBuffers below. The extension binary is
// started fresh for each `bicep local-deploy`, so a buffer holds exactly one
// run's documents. If a process is ever reused across deployments (IDE
// integration, CLI change), stale documents from an earlier run would be
// re-emitted into every subsequent combined file — the failure mode to check
// for if that assumption changes.
//
// Combined documents are buffered in memory and the file is rewritten
// (sorted) on every CreateOrUpdate instead of being appended: Bicep calls
// CreateOrUpdate in an arbitrary order (the extension's resources declare no
// dependencies on each other, so the order varies run to run). An append
// would produce a file whose document order is non-deterministic — unsafe
// for a kubectl-ready manifest (e.g. a ServiceAccount before its
// Namespace). Rewriting keeps the file complete and deterministically
// ordered at all times, and a re-invocation for the same resource replaces
// its document rather than duplicating it.
var (
	combinedMu      sync.Mutex
	combinedBuffers = map[string][]combinedDoc{}
)

// combinedDoc is one buffered YAML document for a combined file.
type combinedDoc struct {
	kind         string
	namespace    string
	name         string
	priority     int
	yaml         string
	explicitName bool // metadata.name was set (false for nameless/generateName-only docs)
}

// resourceMeta describes a resolved Kubernetes resource. namespaced drives
// the metadata.namespace validation in computeManifest.
type resourceMeta struct {
	kind       string
	apiVersion string
	namespaced bool
	known      bool
}

// resolveMeta maps a Bicep resource type name + API version to the
// Kubernetes kind/apiVersion pair. Known catalogue types are resolved
// exactly; anything else (CRDs, preview groups) is passed through with the
// Bicep type prefix used as the API group.
func resolveMeta(bicepType, apiVersion string) resourceMeta {
	if k, ok := k8s.LookupKind(bicepType); ok {
		if apiVersion == "" || apiVersion == k.Version {
			return resourceMeta{kind: k.Kind, apiVersion: k.APIVersion(), namespaced: k.Namespaced, known: true}
		}
	}
	// Fallback: "<group>/<Kind>@<version>" straight through.
	parts := strings.SplitN(bicepType, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return resourceMeta{kind: bicepType, apiVersion: apiVersion, known: false}
	}

	group := parts[0]
	kind := parts[1]

	if apiVersion == "" {
		apiVersion = "v1"
	}

	if group == "core" {
		return resourceMeta{kind: kind, apiVersion: apiVersion, known: false}
	}

	return resourceMeta{kind: kind, apiVersion: fmt.Sprintf("%s/%s", group, apiVersion), namespaced: true, known: false}
}

// manifestResult is the computed manifest for a resource.
type manifestResult struct {
	fileName string
	yaml     string
	body     map[string]any
	meta     resourceMeta
	name     string // metadata.name (sort key for combined documents)
	rawName  string // raw metadata.name ("" for nameless/generateName-only docs)
	ns       string // metadata.namespace ("" for cluster-scoped / absent)
}

// computeManifest validates the resource properties and renders the YAML
// manifest (apiVersion/kind are added by the extension).
func computeManifest(req *resourceRequest) (*manifestResult, error) {
	meta := resolveMeta(req.Type, req.APIVersion)

	var body map[string]any
	if err := json.Unmarshal([]byte(req.Properties), &body); err != nil {
		return nil, fmt.Errorf("failed to parse resource properties as a JSON object: %w", err)
	}

	if body == nil {
		return nil, errors.New("resource properties must be a JSON object (the manifest body)")
	}

	// Bicep expresses "absent property" as null; Kubernetes manifests (and
	// CRD validation) treat absent and null differently, so strip them.
	dropNulls(body)

	// Raw passthrough (k8smanifest/Raw): the body is { document: <manifest> }
	// and the wrapped document is emitted as-is — apiVersion/kind come from
	// the document, not the type string, and everything else (e.g. a sops
	// metadata block) survives verbatim. See k8s.RawBicepType for what this
	// exists for and rawDocument for the wrapper shape.
	source := body

	if k8s.IsRaw(req.Type) {
		var err error

		if source, meta, err = rawDocument(body); err != nil {
			return nil, err
		}
	}

	name, hasName := metadataName(source)
	generateName := metadataGenerateName(source)

	rawName := ""

	if hasName {
		rawName = name
	} else {
		var err error

		name, err = resolveNamelessName(meta, generateName)
		if err != nil {
			return nil, err
		}
	}

	ns := metadataNamespace(source)

	// Scope check: a namespaced kind without metadata.namespace is rejected
	// by the API server, and a cluster-scoped kind with one too. Fallback
	// kinds (unknown CRDs, raw documents) skip the check: their scope is not
	// known (see the README open items).
	if meta.known {
		if meta.namespaced && ns == "" {
			return nil, fmt.Errorf("metadata.namespace is required for namespaced kind %s (it defaults to \"default\" at apply time, so the manifest must say so)", meta.kind)
		}

		if !meta.namespaced && ns != "" {
			return nil, fmt.Errorf("%s is cluster-scoped: metadata.namespace must not be set", meta.kind)
		}
	}

	doc := make(map[string]any, len(source)+2)
	maps.Copy(doc, source)
	doc["apiVersion"] = meta.apiVersion
	doc["kind"] = meta.kind

	content, err := renderYAML(doc)
	if err != nil {
		return nil, err
	}

	return &manifestResult{
		fileName: manifestFileName(meta, ns, name),
		yaml:     content,
		body:     source,
		ns:       ns,
		meta:     meta,
		name:     name,
		rawName:  rawName,
	}, nil
}

// rawDocument unwraps a k8smanifest/Raw body and derives the resource
// metadata from the wrapped document. Bicep resource bodies must be object
// literals (or if/for forms of them), so a manifest loaded with
// loadYamlContent cannot be the body itself — it enters through the single
// document property:
//
//	resource x 'k8smanifest/Raw@v1' = {
//	  document: loadYamlContent('./manifest.yaml')
//	}
//
// The wrapped document is the complete manifest: it must carry its own
// apiVersion and kind, and everything else (the sops metadata block of a
// SOPS-encrypted document, ...) is emitted verbatim. known: false — the
// scope check and the swagger typing apply to catalogue kinds only; a raw
// document's scope is whatever the API server says at apply time. The
// document kind drives the combined-file deploy priority and the
// single-file name, so a raw Secret sorts and names like a real Secret.
func rawDocument(body map[string]any) (map[string]any, resourceMeta, error) {
	if len(body) != 1 {
		return nil, resourceMeta{}, errors.New("a k8smanifest/Raw body must be exactly { document: <manifest> } (Bicep resource bodies are object literals, so a loaded manifest enters through the document property)")
	}

	doc, ok := body["document"].(map[string]any)
	if !ok {
		return nil, resourceMeta{}, errors.New("a k8smanifest/Raw body must be exactly { document: <manifest> } — the document property must hold the manifest object")
	}

	apiVersion, _ := doc["apiVersion"].(string)
	kind, _ := doc["kind"].(string)

	if apiVersion == "" || kind == "" {
		return nil, resourceMeta{}, errors.New("a k8smanifest/Raw document must carry its own apiVersion and kind (the extension does not add them for raw documents)")
	}

	return doc, resourceMeta{kind: kind, apiVersion: apiVersion, known: false}, nil
}

// dropNulls recursively removes null-valued map entries (null = "absent"
// in Bicep). Array elements are left intact.
func dropNulls(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if val == nil {
				delete(t, k)
				continue
			}

			t[k] = dropNulls(val)
		}

		return t
	case []any:
		for i, val := range t {
			t[i] = dropNulls(val)
		}

		return t
	default:
		return v
	}
}

// resolveNamelessName resolves the document name for a body without an
// explicit metadata.name: the sanitised generateName prefix when one is
// given (Kubernetes assigns the concrete name from it at apply time),
// otherwise the nameless placeholder — allowed only for the kinds that
// don't require a name at all.
func resolveNamelessName(meta resourceMeta, generateName string) (string, error) {
	if generateName != "" {
		name := sanitizeName(strings.TrimRight(generateName, "-"))
		if name == "" {
			return namelessDocName, nil
		}

		return name, nil
	}

	// A handful of kinds don't require a name; be lenient there.
	if meta.kind != "Event" && meta.kind != "Binding" && meta.kind != "Status" {
		return "", fmt.Errorf("metadata.name is required for %s resources (or use metadata.generateName)", meta.kind)
	}

	return namelessDocName, nil
}

func metadataName(body map[string]any) (string, bool) {
	md, ok := body["metadata"].(map[string]any)
	if !ok {
		return "", false
	}

	name, ok := md["name"].(string)
	if !ok || name == "" {
		return "", false
	}

	return name, true
}

// metadataGenerateName returns metadata.generateName ("" when absent or not a
// string). Kubernetes accepts generateName in place of name on the kinds that
// assign the concrete name at apply time (Job, Event, ...); which kinds
// support it is backstopped by the API server.
func metadataGenerateName(body map[string]any) string {
	md, ok := body["metadata"].(map[string]any)
	if !ok {
		return ""
	}

	gen, _ := md["generateName"].(string)

	return gen
}

// metadataNamespace returns metadata.namespace ("" when absent or not a
// string).
func metadataNamespace(body map[string]any) string {
	md, ok := body["metadata"].(map[string]any)
	if !ok {
		return ""
	}

	ns, _ := md["namespace"].(string)

	return ns
}

// manifestFileName derives the per-resource file name:
// core kinds -> "<kind>-<name>.yaml", others -> "<group>-<kind>-<name>.yaml",
// with the namespace between the kind and the name for namespaced resources.
// Kubernetes identity is (group, kind, namespace, name), so the namespace is
// part of the file name: two same-named resources in different namespaces
// must not write the same file.
func manifestFileName(meta resourceMeta, ns, name string) string {
	group := ""
	if i := strings.Index(meta.apiVersion, "/"); i >= 0 {
		group = meta.apiVersion[:i]
	}

	middle := ""
	if ns != "" {
		middle = sanitizeName(ns) + "-"
	}

	suffix := fmt.Sprintf("%s-%s%s.yaml", kebabCase(meta.kind), middle, sanitizeName(name))
	if group == "" {
		return suffix
	}

	return fmt.Sprintf("%s-%s", sanitizeName(group), suffix)
}

var (
	// kebabCaseRe splits camelCase boundaries for readable file names.
	kebabCaseRe1 = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	kebabCaseRe2 = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
)

// kebabCase turns a kind name like "HorizontalPodAutoscaler" into
// "horizontal-pod-autoscaler".
func kebabCase(s string) string {
	s = kebabCaseRe1.ReplaceAllString(s, "$1-$2")
	s = kebabCaseRe2.ReplaceAllString(s, "$1-$2")

	return strings.ToLower(s)
}

func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		}

		return '-'
	}, s)
}

// renderYAML serialises a manifest map to deterministic YAML. Top-level keys
// are ordered apiVersion, kind, metadata, spec, then the rest alphabetically;
// nested maps are alphabetical.
func renderYAML(doc map[string]any) (string, error) {
	node := mappingNode(doc, []string{"apiVersion", "kind", "metadata", "spec"})

	var buf bytes.Buffer

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	if err := enc.Encode(node); err != nil {
		return "", fmt.Errorf("failed to render YAML: %w", err)
	}

	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("failed to render YAML: %w", err)
	}

	return buf.String(), nil
}

func mappingNode(m map[string]any, preferred []string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	emit := func(k string) {
		// Keys go through stringNode too: a YAML 1.1 boolean token as a key
		// (ConfigMap data key "yes") must be quoted, not just values.
		node.Content = append(node.Content,
			stringNode(k),
			valueNode(m[k]),
		)
	}
	seen := map[string]bool{}

	for _, k := range preferred {
		if _, ok := m[k]; ok {
			emit(k)
			seen[k] = true
		}
	}

	var rest []string

	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}

	sort.Strings(rest)

	for _, k := range rest {
		emit(k)
	}

	return node
}

// yaml.v3 does not double-quote strings that a YAML 1.1 reader would
// resolve as a non-string (booleans, numbers, null). Kubernetes parses
// manifests with YAML 1.1 semantics (sigs.k8s.io/yaml), so such values must
// be explicitly quoted or ConfigMap data like "yes" becomes boolean true.
var (
	yamlBoolRe     = regexp.MustCompile(`^(?:y|Y|yes|Yes|YES|n|N|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF)$`)
	yamlNullRe     = regexp.MustCompile(`^(?:~|null|Null|NULL)$`)
	yamlNumRe      = regexp.MustCompile(`^(?:[-+]?[0-9][0-9_]*(?:\.[0-9_]*)?(?:[eE][-+]?[0-9]+)?|0[xob][0-9a-fA-F_]+|\.(?:inf|Inf|INF|nan|NaN|NAN))$`)
	yamlIndicators = "!&*?|>%@#`"
)

func needsQuoting(s string) bool {
	if s == "" || isYAML11NonString(s) || s != strings.Trim(s, " \t") || strings.Contains(s, " #") {
		return true
	}

	return hasLeadingIndicator(s)
}

func isYAML11NonString(s string) bool {
	return yamlBoolRe.MatchString(s) || yamlNullRe.MatchString(s) || yamlNumRe.MatchString(s)
}

func hasLeadingIndicator(s string) bool {
	first := rune(s[0])
	switch {
	case strings.ContainsRune(yamlIndicators, first):
		return true
	case first == '-' && (len(s) == 1 || s[1] == ' '):
		return true
	case first == ':' && (len(s) == 1 || s[1] == ' '):
		return true
	case first == '?' && (len(s) == 1 || s[1] == ' '):
		return true
	}

	return false
}

func stringNode(s string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	if needsQuoting(s) {
		node.Style = yaml.DoubleQuotedStyle
	}

	return node
}

func valueNode(v any) *yaml.Node {
	switch t := v.(type) {
	case map[string]any:
		return mappingNode(t, nil)
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range t {
			node.Content = append(node.Content, valueNode(item))
		}

		return node
	case string:
		return stringNode(t)
	case bool:
		val := "false"
		if t {
			val = "true"
		}

		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: val}
	case float64:
		// json.Unmarshal decodes every JSON number to float64; integral
		// values (safe up to 2^53) are rendered as ints so manifests stay
		// type-correct (replicas: 2, not replicas: "2").
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			v := strconv.FormatInt(int64(t), 10)

			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: v}
		}

		// YAML 1.1 (sigs.k8s.io/yaml, used by kubectl) resolves an exponent
		// float without a mantissa dot ("1e-07") as a *string*; adding the
		// dot ("1.0e-07") keeps it a number under both 1.1 and 1.2. (Quoting
		// would not help: a quoted scalar is a string by definition.)
		v := strconv.FormatFloat(t, 'g', -1, 64)
		if i := strings.IndexAny(v, "eE"); i >= 0 && !strings.Contains(v, ".") {
			v = v[:i] + ".0" + v[i:]
		}

		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: v}
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	default:
		// json.Unmarshal produces only the types above when decoding into
		// any; anything else is stringified defensively.
		return stringNode(fmt.Sprintf("%v", t))
	}
}

// resultPath returns the file a manifest result will be written to (used
// for both the real write and the preview's planned path).
func resultPath(cfg *extensionConfig, r *manifestResult) string {
	if cfg.Combine {
		return filepath.Join(outputDir(cfg), combinedFileName)
	}

	return filepath.Join(outputDir(cfg), r.fileName)
}

// deployPriority returns the document order for combined files: lower first.
// The order follows the classic kubectl/helm deploy sequence — the
// namespace before what goes into it, configuration and identity
// (ConfigMap/Secret/ServiceAccount/RBAC/storage/quotas) before the workloads
// that reference them, services before workloads and routes, workloads
// before the autoscalers that target them, and unknown kinds (CRD
// instances, preview API groups) last — after the CRDs that may define them.
var deployPriorityByKind = map[string]int{
	"Namespace": 0,

	"PriorityClass":       1,
	"StorageClass":        1,
	"CSIDriver":           1,
	"CSINode":             1,
	"VolumeSnapshotClass": 1,
	"APIService":          1,

	"CustomResourceDefinition": 2,

	"Role":        3,
	"ClusterRole": 3,

	"RoleBinding":        4,
	"ClusterRoleBinding": 4,

	"ServiceAccount": 5,

	"ConfigMap": 6,
	"Secret":    6,

	"PersistentVolume":      7,
	"PersistentVolumeClaim": 7,

	"LimitRange":    8,
	"ResourceQuota": 8,

	"NetworkPolicy":       9,
	"CiliumNetworkPolicy": 9,

	"IngressClass": 10,
	"Service":      10,

	"Gateway": 11,

	"Pod":                   12,
	"PodTemplate":           12,
	"DaemonSet":             12,
	"StatefulSet":           12,
	"ReplicaSet":            12,
	"ReplicationController": 12,
	"Deployment":            12,

	"Job":     13,
	"CronJob": 13,

	"Ingress":   14,
	"HTTPRoute": 14,

	"HorizontalPodAutoscaler": 15,

	"PodDisruptionBudget": 16,
	"Widget":              16, // the examples' fixture CR (its CRD is priority 2)
}

// defaultDeployPriority orders unknown kinds last (after the CRDs that may
// define them).
const defaultDeployPriority = 17

func deployPriority(kind string) int {
	if p, ok := deployPriorityByKind[kind]; ok {
		return p
	}

	return defaultDeployPriority
}

// singleFileMu guards singleFileOwners below. The extension binary is
// started fresh for each `bicep local-deploy`, so the map holds exactly one
// run's files: re-running the same template stays idempotent (a
// re-invocation overwrites its own file), while a *different* resource that
// maps onto an already-written file must not silently clobber it.
var (
	singleFileMu     sync.Mutex
	singleFileOwners = map[string]singleFileOwner{}
)

// singleFileOwner records which resource owns a single-file output for this
// run. name is the raw metadata.name ("" for nameless/generateName-only
// documents), so "a_b" and "a-b" stay distinct identities even though they
// sanitize to the same file name.
type singleFileOwner struct {
	kind string
	ns   string
	name string
	yaml string
}

// fileKey namespaces a file name by its output directory.
func fileKey(dir, fileName string) string {
	return dir + "\x00" + fileName
}

// writeManifest writes the YAML for a computed manifest: one file per
// resource, or into the single sorted combined file when cfg.Combine is set.
func writeManifest(cfg *extensionConfig, r *manifestResult) (string, error) {
	if cfg.Combine {
		return writeCombinedManifest(cfg, r)
	}

	dir := outputDir(cfg)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("failed to create output directory %q: %w", dir, err)
	}

	singleFileMu.Lock()
	defer singleFileMu.Unlock()

	fileName := r.fileName
	owner := singleFileOwner{kind: r.meta.kind, ns: r.ns, name: r.rawName, yaml: r.yaml}

	if prev, ok := singleFileOwners[fileKey(dir, fileName)]; ok {
		switch {
		case prev.yaml == owner.yaml:
			// identical re-invocation: nothing to do (the file already holds
			// exactly this document)
		case owner.name != "":
			// a distinct named resource maps onto this file — either two
			// resources with the same (namespace, name) identity, or names
			// that sanitize to the same slug ("a_b" and "a-b"). Fail rather
			// than clobber: within one run the file is already owned.
			return "", fmt.Errorf("two resources map to the same file %q: %s %s/%s and %s %s/%s — give them distinct metadata.name values or set combine: true",
				fileName, prev.kind, prev.ns, prev.name, owner.kind, owner.ns, owner.name)
		default:
			// nameless documents (no explicit metadata.name) share one file per
			// (kind, namespace): disambiguate like combined mode so every
			// document survives.
			disambiguated, err := disambiguateSingleFileName(dir, fileName, owner)
			if err != nil {
				return "", err
			}

			fileName = disambiguated
		}
	}

	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte(r.yaml), 0o600); err != nil {
		return "", fmt.Errorf("failed to write manifest file %q: %w", path, err)
	}

	singleFileOwners[fileKey(dir, fileName)] = owner

	return path, nil
}

// disambiguateSingleFileName returns "<base>_2.yaml" (then _3, ...) for a
// file already owned by a different nameless document, mirroring the
// combined mode's unnamed_2/unnamed_3 re-keying.
func disambiguateSingleFileName(dir, fileName string, owner singleFileOwner) (string, error) {
	base := strings.TrimSuffix(fileName, ".yaml")

	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s_%d.yaml", base, n)
		if prev, ok := singleFileOwners[fileKey(dir, candidate)]; !ok || prev.yaml == owner.yaml {
			return candidate, nil
		}
	}
}

// upsertCombinedDoc replaces the buffer's document with the same kind,
// namespace and name (a re-invocation must not duplicate it), otherwise
// appends. Exception: distinct documents without an explicit metadata.name
// (nameless kinds, or generateName-only resources) may share a key — for
// those, replacement would silently drop the first document, so the new one
// is re-keyed under a fresh name ("unnamed_2", "probe_job_2", ...) instead.
func upsertCombinedDoc(docs []combinedDoc, doc combinedDoc) []combinedDoc {
	for i, d := range docs {
		if d.kind != doc.kind || d.namespace != doc.namespace || d.name != doc.name {
			continue
		}

		if d.yaml == doc.yaml {
			return docs
		}

		// A re-invocation of the same named resource: replace.
		if doc.explicitName {
			docs[i] = doc

			return docs
		}

		// Distinct nameless documents: re-key rather than replace, so both
		// survive. The disambiguated name is buffer bookkeeping only; the
		// document's YAML is unchanged (kubectl keys on the manifest's own
		// metadata, which nameless kinds do not have).
		for n := 1; ; n++ {
			candidate := doc.name + "_" + strconv.Itoa(n)
			if !docKeyExists(docs, doc.kind, doc.namespace, candidate) {
				doc.name = candidate

				return append(docs, doc)
			}
		}
	}

	return append(docs, doc)
}

func docKeyExists(docs []combinedDoc, kind, namespace, name string) bool {
	for _, d := range docs {
		if d.kind == kind && d.namespace == namespace && d.name == name {
			return true
		}
	}

	return false
}

// writeCombinedManifest adds the resource's document to the per-directory
// buffer, sorts the buffer by deploy priority, and rewrites the combined
// file (documents separated by `---`). The buffer is only committed after
// the file write succeeds, so a failed write cannot desynchronise the
// in-memory buffer from what is on disk.
func writeCombinedManifest(cfg *extensionConfig, r *manifestResult) (string, error) {
	combinedMu.Lock()
	defer combinedMu.Unlock()

	dir := outputDir(cfg)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("failed to create output directory %q: %w", dir, err)
	}

	// Namespaced kinds carry the namespace in the dedupe key: the same
	// policy name may exist in different namespaces (see the internal
	// network-policy module, which emits identically-named policies per
	// tenant namespace in one run). Documents without an explicit
	// metadata.name (nameless or generateName-only) are re-keyed by
	// upsertCombinedDoc so that distinct documents all survive.
	doc := combinedDoc{
		kind:         r.meta.kind,
		namespace:    r.ns,
		name:         r.name,
		priority:     deployPriority(r.meta.kind),
		yaml:         r.yaml,
		explicitName: r.rawName != "",
	}

	// Work on a copy so a failed write below cannot leave the committed
	// buffer accepting a document the file never got.
	docs := make([]combinedDoc, len(combinedBuffers[dir]))
	copy(docs, combinedBuffers[dir])
	docs = upsertCombinedDoc(docs, doc)

	sort.SliceStable(docs, func(i, j int) bool {
		if docs[i].priority != docs[j].priority {
			return docs[i].priority < docs[j].priority
		}

		if docs[i].kind != docs[j].kind {
			return docs[i].kind < docs[j].kind
		}

		// The namespace tiebreaker makes same-kind/same-name docs in
		// different namespaces deterministic regardless of the order the
		// extension received the resource requests in (byte-stable output).
		if docs[i].namespace != docs[j].namespace {
			return docs[i].namespace < docs[j].namespace
		}

		return docs[i].name < docs[j].name
	})

	var buf bytes.Buffer

	for i, d := range docs {
		if i > 0 {
			buf.WriteString("---\n")
		}

		buf.WriteString(d.yaml)
	}

	path := filepath.Join(dir, combinedFileName)

	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("failed to write combined manifest file %q: %w", path, err)
	}

	combinedBuffers[dir] = docs

	return path, nil
}
