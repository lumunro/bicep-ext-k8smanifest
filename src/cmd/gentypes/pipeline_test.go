// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

// Pipeline tests for the generator: run() executed in-process against the
// committed swagger + CRD schemas, compared against the committed gen/
// golden files; unit tests for the merge/body/required helpers; and the
// deterministicOutput corner cases (slice values, cross-file refs, ref
// cycles).
package main

import (
	"bicep-ext-k8smanifest/k8s"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Azure/bicep-types/src/bicep-types-go/types"
)

// TestRunMatchesCommittedGen is the end-to-end invariant: the committed
// swagger + CRD schemas are the pinned inputs and the committed gen/ files
// are the golden output. Regenerating in-process must reproduce types.json
// byte-for-byte and index.json modulo settings.version (the committed file
// carries the released version; the test stamps "test").
func TestRunMatchesCommittedGen(t *testing.T) {
	t.Parallel()

	out := t.TempDir()

	if err := run("../../../swagger/swagger.json", out, "test"); err != nil {
		t.Fatalf("run: %v", err)
	}

	gotTypes, err := os.ReadFile(filepath.Join(out, "types.json")) // #nosec G304 -- fixed test path
	if err != nil {
		t.Fatalf("reading generated types.json: %v", err)
	}

	wantTypes, err := os.ReadFile(filepath.Join("..", "..", "gen", "types.json"))
	if err != nil {
		t.Fatalf("reading committed gen/types.json: %v", err)
	}

	if !bytes.Equal(gotTypes, wantTypes) {
		t.Errorf("types.json differs from the committed golden file (got %d bytes, want %d) — commit the regenerated gen/ files",
			len(gotTypes), len(wantTypes))
	}

	if got, want := indexBytes(t, out), indexBytes(t, filepath.Join("..", "..", "gen")); !bytes.Equal(got, want) {
		t.Errorf("index.json differs from the committed golden file (modulo settings.version):\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// indexBytes returns the index file as canonical JSON with settings.version
// normalised, so two generations of the same swagger compare equal
// regardless of the stamped version (json.Marshal sorts map keys).
func indexBytes(t *testing.T, dir string) []byte {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(dir, "index.json")) // #nosec G304 -- fixed test path
	if err != nil {
		t.Fatalf("reading %s/index.json: %v", dir, err)
	}

	var idx map[string]any

	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatalf("unmarshal index: %v", err)
	}

	settings, ok := idx["settings"].(map[string]any)
	if !ok {
		t.Fatal("index has no settings object")
	}

	settings["version"] = "x"

	out, err := json.Marshal(idx)
	if err != nil {
		t.Fatalf("canonicalise index: %v", err)
	}

	return out
}

// TestSwaggerDefName pins the group -> Go package path layout for the
// representative shapes (core, regular group, CRD-merged group, unknown
// group).
func TestSwaggerDefName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind k8s.Kind
		want string
	}{
		{k8s.Kind{Kind: "Service", Version: "v1"}, "io.k8s.api.core.v1.Service"},
		{k8s.Kind{Kind: "Deployment", Group: k8s.GrpApps, Version: "v1"}, "io.k8s.api.apps.v1.Deployment"},
		{k8s.Kind{Kind: "Gateway", Group: k8s.GrpGateway, Version: "v1"}, "io.k8s.api.gateway.networking.k8s.io.v1.Gateway"},
		{k8s.Kind{Kind: "CiliumNetworkPolicy", Group: k8s.GrpCilium, Version: "v2"}, "io.k8s.api.cilium.io.v2.CiliumNetworkPolicy"},
		{k8s.Kind{Kind: "Widget", Group: k8s.GrpExample, Version: "v1"}, "io.k8s.api.example.com.v1.Widget"},
		{k8s.Kind{Kind: "Gadget", Group: "acme.io", Version: "v1"}, ""},
	}

	for _, tc := range cases {
		if got := swaggerDefName(tc.kind); got != tc.want {
			t.Errorf("swaggerDefName(%+v) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// TestMergeExtraDefinitions: missing directory is a no-op (the CRD kinds
// fall back to permissive), valid files merge, parse failures and
// definition-name collisions are hard errors, non-JSON files are ignored.
func TestMergeExtraDefinitions(t *testing.T) {
	t.Parallel()

	defs := map[string]*schema{}

	if err := mergeExtraDefinitions(filepath.Join(t.TempDir(), "no-such-dir"), defs); err != nil {
		t.Fatalf("missing crd dir must not be an error: %v", err)
	}

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "crd.json"), []byte(`{"definitions":{"io.k8s.api.x.v1.Widget":{"type":"object"}}}`), 0o600); err != nil {
		t.Fatalf("writing crd.json: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("writing ignore.txt: %v", err)
	}

	if err := mergeExtraDefinitions(dir, defs); err != nil {
		t.Fatalf("merge: %v", err)
	}

	if _, ok := defs["io.k8s.api.x.v1.Widget"]; !ok {
		t.Error("definition from crd.json not merged")
	}

	bad := t.TempDir()

	if err := os.WriteFile(filepath.Join(bad, "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("writing bad.json: %v", err)
	}

	if err := mergeExtraDefinitions(bad, map[string]*schema{}); err == nil {
		t.Error("malformed CRD JSON must be an error")
	}

	coll := t.TempDir()

	if err := os.WriteFile(filepath.Join(coll, "dup.json"), []byte(`{"definitions":{"io.k8s.api.core.v1.Service":{"type":"object"}}}`), 0o600); err != nil {
		t.Fatalf("writing dup.json: %v", err)
	}

	err := mergeExtraDefinitions(coll, map[string]*schema{"io.k8s.api.core.v1.Service": {}})
	if err == nil || !strings.Contains(err.Error(), "present in both") {
		t.Errorf("definition-name collision must be a hard error, got %v", err)
	}
}

// TestBodyFor pins the body contract: envelope properties (apiVersion,
// kind, status) are stripped from the swagger shape (the extension adds
// apiVersion/kind; status is server-managed) and re-added only as read-only
// response properties; metadata and spec are required (spec unless the kind
// is in optionalSpec); swagger-required root properties carry through.
func TestBodyFor(t *testing.T) {
	t.Parallel()

	g := newGenerator(map[string]*schema{
		"io.k8s.api.core.v1.Service": {
			Type: schemaTypeObject,
			Properties: map[string]*schema{
				"apiVersion": {Type: schemaTypeString},
				"kind":       {Type: schemaTypeString},
				"metadata":   {Type: schemaTypeObject, Properties: map[string]*schema{"name": {Type: schemaTypeString}}},
				"spec":       {Type: schemaTypeObject, Properties: map[string]*schema{"ports": {Type: schemaTypeArray}}},
				"status":     {Type: schemaTypeObject},
				"extra":      {Type: schemaTypeString},
			},
			Required: []string{"extra", "bogus"},
		},
	})

	ref, typed := g.bodyFor(k8s.Kind{Kind: "Service", Version: "v1"})
	if !typed {
		t.Fatal("Service has a definition; bodyFor must report typed")
	}

	obj := mustBe[*types.ObjectType](t, typeAt(t, g, ref), "Service body")

	if _, ok := obj.Properties["status"]; ok {
		t.Error("status must be stripped (server-managed)")
	}

	if _, ok := obj.Properties["extra"]; !ok {
		t.Error("extra property missing from the body")
	}

	if got := obj.Properties["metadata"].Flags; got != types.TypePropertyFlagsRequired {
		t.Errorf("metadata flags = %v, want required", got)
	}

	if got := obj.Properties["spec"].Flags; got != types.TypePropertyFlagsRequired {
		t.Errorf("spec flags = %v, want required (Service is not in optionalSpec)", got)
	}
	// swagger-required names carry through; names absent from the body are
	// ignored (a stale "required" entry must not create a phantom property).
	if got := obj.Properties["extra"].Flags; got != types.TypePropertyFlagsRequired {
		t.Errorf("extra flags = %v, want required (swagger required)", got)
	}

	if _, ok := obj.Properties["bogus"]; ok {
		t.Error("bogus (required but absent) must not appear in the body")
	}

	// The read-only response properties are added to the body type;
	// apiVersion/kind come back this way (stripped from the swagger shape
	// above).
	for _, name := range []string{"filePath", "content", "apiVersion", "kind"} {
		p, ok := obj.Properties[name]
		if !ok {
			t.Errorf("response property %q missing from the body", name)
			continue
		}

		if p.Flags != types.TypePropertyFlagsReadOnly {
			t.Errorf("response property %q flags = %v, want readOnly", name, p.Flags)
		}
	}

	// A kind with no definition falls back to the permissive body.
	if _, typed := g.bodyFor(k8s.Kind{Kind: "Widget", Version: "v1"}); typed {
		t.Error("kind without a definition must fall back to the permissive body")
	}
}

// TestDeterministicOutputSlicesAndCrossRefs covers the canonSlice path (a
// slice-valued entry field), the cross-file ref remap and the ref-cycle
// guard in the sort-key canonicalisation.
func TestDeterministicOutputSlicesAndCrossRefs(t *testing.T) {
	t.Parallel()

	typesIn := "[\n" +
		`  {"$type": "UnionType", "name": "U", "elements": [2, 1]},` + "\n" +
		`  {"$type": "StringType"}` + "\n" +
		"]"

	// The cross-file ref pointed at the StringType (old position 1) and must
	// follow it to its new position 0. The input uses the writer's
	// MarshalIndent layout ("$ref": "...") — remapCrossRefs matches that
	// exact key-value shape on purpose.
	indexIn := `{"resources":{"a/B@v1":{"relativePath":"types.json","$ref": "types.json#/1"}}}`

	outTypes, outIndex, err := deterministicOutput(typesIn, indexIn)
	if err != nil {
		t.Fatalf("deterministicOutput: %v", err)
	}

	// Sort: the anonymous StringType (name "") precedes "U".
	iStr, iU := strings.Index(outTypes, `"StringType"`), strings.Index(outTypes, `"name": "U"`)
	if iStr < 0 || iU < 0 || iStr > iU {
		t.Errorf("sort order wrong (StringType=%d, U=%d):\n%s", iStr, iU, outTypes)
	}

	if !strings.Contains(outTypes, `"elements": [2, 1]`) {
		t.Errorf("slice entry text was corrupted:\n%s", outTypes)
	}

	// The cross-file ref pointed at the StringType (old position 1) and must
	// follow it to its new position 0.
	if want := `{"resources":{"a/B@v1":{"relativePath":"types.json","$ref": "types.json#/0"}}}`; outIndex != want {
		t.Errorf("cross-file ref not remapped:\ngot:  %s\nwant: %s", outIndex, want)
	}

	// A ref cycle in the sort-key canonicalisation must not hang: the
	// visiting set short-circuits it.
	cycle := "[\n" +
		`  {"$type": "A", "d": {"$ref": "#/1"}},` + "\n" +
		`  {"$type": "B", "d": {"$ref": "#/0"}}` + "\n" +
		"]"

	if _, _, err := deterministicOutput(cycle, ""); err != nil {
		t.Errorf("ref cycle must terminate: %v", err)
	}
}

// TestGeneratorHelpers: memoised primitive refs, warn(), and unionOf
// de-duplication (a repeated member must appear once).
func TestGeneratorHelpers(t *testing.T) {
	t.Parallel()

	g := newGenerator(nil)

	g.warn("a warning")

	if len(g.warnings) != 1 || g.warnings[0] != "a warning" {
		t.Errorf("warnings = %v, want [a warning]", g.warnings)
	}

	b1 := g.boolRef()
	b2 := g.boolRef()

	if typeAt(t, g, b1).Type() != "BooleanType" {
		t.Errorf("boolRef = %s, want BooleanType", typeAt(t, g, b1).Type())
	}

	if refID(b1) != refID(b2) {
		t.Error("boolRef is not memoised")
	}

	// Two identical refs collapse: the dedup leaves one member and unionOf
	// returns that member itself rather than a one-element union.
	u := g.unionOf(g.stringRef(), g.stringRef())
	if got := typeAt(t, g, u).Type(); got != "StringType" {
		t.Errorf("union of two identical refs = %s, want StringType (the member itself)", got)
	}

	// Distinct refs still produce a real union with both members.
	du := g.unionOf(g.stringRef(), g.integerRef())

	ut := mustBe[*types.UnionType](t, typeAt(t, g, du), "distinct-refs union")

	if len(ut.Elements) != 2 {
		t.Errorf("union of two distinct refs has %d elements, want 2", len(ut.Elements))
	}
}
