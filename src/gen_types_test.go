// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

package main

import (
	"bicep-ext-k8smanifest/k8s"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// These tests verify the swagger-generated Bicep type files embedded into
// the binary (see cmd/gentypes). They run against the committed gen/ files,
// so a fresh checkout passes without the swagger present; after changing the
// swagger or the generator, re-run `go run ./cmd/gentypes` first.

type genTypes struct {
	index map[string]any
	file  []map[string]any
}

func loadGenTypes(t *testing.T) genTypes {
	t.Helper()

	indexContent, typeFiles := loadTypeFiles()

	var index map[string]any
	if err := json.Unmarshal([]byte(indexContent), &index); err != nil {
		t.Fatalf("index.json does not parse: %v", err)
	}

	typesJSON, ok := typeFiles[typesFileName]
	if !ok {
		t.Fatalf("type files missing %s (got %v)", typesFileName, keys(typeFiles))
	}

	var file []map[string]any
	if err := json.Unmarshal([]byte(typesJSON), &file); err != nil {
		t.Fatalf("%s does not parse: %v", typesFileName, err)
	}

	return genTypes{index: index, file: file}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

// byName finds a named type in the type file.
func (g genTypes) byName(t *testing.T, name string) map[string]any {
	t.Helper()

	if obj, ok := namedObject(g.file, name); ok {
		return obj
	}

	t.Fatalf("no ObjectType named %q in %s", name, typesFileName)

	return nil
}

// resolve follows a "$ref" (same-file "#/N" form) to the referenced type.
func (g genTypes) resolve(t *testing.T, ref any) map[string]any {
	t.Helper()

	m, ok := ref.(map[string]any)
	if !ok {
		t.Fatalf("ref is not an object: %v", ref)
	}

	ptr, _ := m["$ref"].(string)
	if !strings.HasPrefix(ptr, "#/") {
		t.Fatalf("expected a same-file $ref, got %q", ptr)
	}

	i, err := strconv.Atoi(ptr[2:])
	if err != nil || i < 0 || i >= len(g.file) {
		t.Fatalf("$ref %q out of range", ptr)
	}

	return g.file[i]
}

func propsOf(t *testing.T, obj map[string]any) map[string]map[string]any {
	t.Helper()

	raw, ok := obj["properties"].(map[string]any)
	if !ok {
		t.Fatalf("object has no properties: %v", obj[nameKey])
	}

	out := make(map[string]map[string]any, len(raw))
	for k, v := range raw {
		p, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("property %s is not an object", k)
		}

		out[k] = p
	}

	return out
}

func flagOf(t *testing.T, p map[string]any) int64 {
	t.Helper()

	f, ok := p["flags"].(float64)
	if !ok {
		t.Fatalf("property has no numeric flags: %v", p)
	}

	return int64(f)
}

// TestIndexCoversAllCataloguedKinds: every catalogue kind is in the type
// index, keyed by "<group>/<Kind>@<version>", pointing into types.json.
func TestIndexCoversAllCataloguedKinds(t *testing.T) {
	t.Parallel()

	g := loadGenTypes(t)

	resources, ok := g.index["resources"].(map[string]any)
	if !ok {
		t.Fatal("index has no resources")
	}

	for _, k := range k8s.Kinds {
		ref, ok := resources[k.BicepTypeReference()]
		if !ok {
			t.Errorf("index missing resource %s", k.BicepTypeReference())

			continue
		}

		m, _ := ref.(map[string]any)

		ptr, _ := m["$ref"].(string)
		if !strings.HasPrefix(ptr, typesFileName+"#/") {
			t.Errorf("resource %s ref %q must be a cross-file ref into %s", k.BicepTypeReference(), ptr, typesFileName)
		}
	}

	fallback, _ := g.index["fallbackResourceType"].(map[string]any)
	if got, _ := fallback["$ref"].(string); !strings.HasPrefix(got, typesFileName+"#/") {
		t.Errorf("fallbackResourceType ref %q must point into %s", got, typesFileName)
	}
}

// TestRawTypeHandAuthored: k8smanifest/Raw@v1 is a first-class
// (hand-authored) resource type in the index — not a Kubernetes kind — so
// the compiler type-checks its body and no BCP081 is reported. The body
// is the strict wrapper { document: <any> }: exactly one property of type
// AnyType, and a closed object (no additionalProperties) so a stray
// property beside document is a compile error.
func TestRawTypeHandAuthored(t *testing.T) {
	t.Parallel()

	g := loadGenTypes(t)

	resources, ok := g.index["resources"].(map[string]any)
	if !ok {
		t.Fatal("index has no resources")
	}

	const rawRef = "k8smanifest/Raw@v1"
	ref, ok := resources[rawRef]
	if !ok {
		t.Fatalf("index missing resource %s", rawRef)
	}

	m, _ := ref.(map[string]any)
	ptr, _ := m["$ref"].(string)
	if !strings.HasPrefix(ptr, typesFileName+"#/") {
		t.Fatalf("resource %s ref %q must be a cross-file ref into %s", rawRef, ptr, typesFileName)
	}

	i, err := strconv.Atoi(strings.TrimPrefix(ptr, typesFileName+"#/"))
	if err != nil || i < 0 || i >= len(g.file) {
		t.Fatalf("$ref %q out of range", ptr)
	}

	resource := g.file[i]
	if resource["$type"] != "ResourceType" || resource["name"] != rawRef {
		t.Fatalf("resource entry is not the Raw resource type: %v", resource)
	}

	bodyRef, ok := resource["body"].(map[string]any)
	if !ok {
		t.Fatal("resource type has no body ref")
	}

	body := g.resolve(t, bodyRef)
	if body["$type"] != "ObjectType" || body["name"] != "K8sManifestRawBody" {
		t.Fatalf("body is not the K8sManifestRawBody object type: %v", body)
	}

	if _, hasExtra := body["additionalProperties"]; hasExtra {
		t.Error("Raw body must be a closed object (no additionalProperties)")
	}

	props := propsOf(t, body)
	if len(props) != 1 {
		t.Fatalf("Raw body must have exactly one property, has %d", len(props))
	}

	doc, ok := props["document"]
	if !ok {
		t.Fatal("Raw body has no document property")
	}

	docType := g.resolve(t, doc["type"])
	if docType["$type"] != "AnyType" {
		t.Errorf("document property must be AnyType (the wrapper keeps the manifest opaque), got %v", docType)
	}
}

// TestDeploymentBodyRequired: the typed body carries the mandatory minimum
// (metadata + spec) and the read-only response properties.
func TestDeploymentBodyRequired(t *testing.T) {
	t.Parallel()

	g := loadGenTypes(t)

	body := g.byName(t, "io.k8s.api.apps.v1.DeploymentBody")
	props := propsOf(t, body)

	for _, name := range []string{metadataKey, "spec"} {
		if got := flagOf(t, props[name]); got&1 == 0 {
			t.Errorf("Deployment body property %s is not required (flags %d)", name, got)
		}
	}

	for _, name := range []string{"filePath", "content", apiVersionKey, kindKey} {
		if _, ok := props[name]; !ok {
			t.Errorf("Deployment body missing response property %s", name)
		}
	}

	// metadata.name is optional at the type level (the runtime requires
	// metadata.name or metadata.generateName for named kinds, and BCP035 for
	// template-level mistakes stays the API server's job), and described.
	metaRef := g.resolve(t, props[metadataKey]["type"])
	if metaRef["$type"] != "ObjectType" {
		t.Fatalf("Deployment.metadata is not an ObjectType: %v", metaRef["$type"])
	}

	meta := propsOf(t, metaRef)
	if got := flagOf(t, meta[nameKey]); got&1 != 0 {
		t.Errorf("ObjectMeta.name should be optional (flags %d) — the runtime accepts metadata.generateName in place of it", got)
	}

	if desc, _ := meta[nameKey]["description"].(string); desc == "" {
		t.Error("ObjectMeta.name has no description (hover text)")
	}

	if _, ok := meta["generateName"]; !ok {
		t.Error("ObjectMeta has no generateName property")
	}
}

// TestPodTemplateMetadataNameOptional: pod template metadata must NOT
// require a name (it is optional in the API). The shared ObjectMeta type
// does not require name at all (so metadata.generateName passes compile
// time; the runtime enforces name-or-generateName), so this guards the
// regression from the other direction.
func TestPodTemplateMetadataNameOptional(t *testing.T) {
	t.Parallel()

	g := loadGenTypes(t)

	podTemplate := g.byName(t, "io.k8s.api.core.v1.PodTemplateSpec")

	metaRef := g.resolve(t, propsOf(t, podTemplate)[metadataKey]["type"])
	if metaRef["$type"] != "ObjectType" {
		t.Fatalf("PodTemplateSpec.metadata is not an ObjectType")
	}

	meta := propsOf(t, metaRef)

	if got := flagOf(t, meta[nameKey]); got&1 != 0 {
		t.Errorf("PodTemplateSpec.metadata.name should be optional (flags %d)", got)
	}
}

// TestIntOrStringUnion: IntOrString fields are string | int.
func TestIntOrStringUnion(t *testing.T) {
	t.Parallel()

	g := loadGenTypes(t)

	found := false

	for _, entry := range g.file {
		if entry["$type"] != "UnionType" {
			continue
		}

		elems, _ := entry["elements"].([]any)
		if len(elems) != 2 {
			continue
		}

		kinds := map[string]bool{}

		for _, e := range elems {
			kind, _ := g.resolve(t, e)["$type"].(string) // #nosec errcheck -- $type is always a string discriminator
			kinds[kind] = true
		}

		if kinds["StringType"] && kinds["IntegerType"] {
			found = true

			break
		}
	}

	if !found {
		t.Error("no UnionType of (StringType, IntegerType) found — IntOrString not modelled")
	}
}

// TestMandatorySpecProperties: a spot-check of the curated required overlay.
func TestMandatorySpecProperties(t *testing.T) {
	t.Parallel()

	g := loadGenTypes(t)

	cases := []struct {
		object, prop string
	}{
		{"io.k8s.api.core.v1.ServiceSpec", "ports"},
		{"io.k8s.api.core.v1.ContainerPort", "containerPort"},
		{"io.k8s.api.core.v1.PodSpec", "containers"},
		{"io.k8s.api.core.v1.Container", "image"},
		{"io.k8s.api.rbac.v1.RoleRef", nameKey},
		{"io.k8s.api.autoscaling.v2.HorizontalPodAutoscalerSpec", "scaleTargetRef"},
	}

	for _, c := range cases {
		obj, ok := namedObject(g.file, c.object)
		if !ok {
			t.Errorf("object %s not generated", c.object)

			continue
		}

		props := propsOf(t, obj)

		p, ok := props[c.prop]
		if !ok {
			t.Errorf("%s has no property %s", c.object, c.prop)

			continue
		}

		if got := flagOf(t, p); got&1 == 0 {
			t.Errorf("%s.%s is not required (flags %d)", c.object, c.prop, got)
		}
	}
}

// TestOptionalAPIPropertiesNotOverRequired guards the required-overlay
// regression: these fields are optional in the Kubernetes API (defaults
// exist / validation permits absence) and must NOT be marked required, or
// valid manifests fail at Bicep compile time.
func TestOptionalAPIPropertiesNotOverRequired(t *testing.T) {
	t.Parallel()

	g := loadGenTypes(t)

	cases := []struct {
		object, prop string
	}{
		{"io.k8s.api.autoscaling.v2.HorizontalPodAutoscalerSpec", "minReplicas"}, // defaults to 1
		{"io.k8s.api.networking.v1.IngressSpec", "rules"},                        // defaultBackend-only Ingress is valid
		{"io.k8s.api.core.v1.Toleration", "effect"},                              // empty = match all effects
		// 2026-09-14 server-side dry-run probes: the API accepts these absent.
		// (The RBAC *Spec types are inlined in this swagger, so the flags live
		// on the root body objects; events v1 keeps its fields on the body too.)
		{"io.k8s.api.rbac.v1.RoleBindingBody", "subjects"},
		{"io.k8s.api.rbac.v1.ClusterRoleBindingBody", "subjects"},
		{"io.k8s.api.coordination.v1.LeaseSpec", "holderIdentity"},
		{"io.k8s.api.events.v1.EventBody", "note"}, // reason and eventTime are the mandatory pair
	}

	for _, c := range cases {
		obj, ok := namedObject(g.file, c.object)
		if !ok {
			t.Errorf("object %s not generated", c.object)

			continue
		}

		props := propsOf(t, obj)

		p, ok := props[c.prop]
		if !ok {
			t.Errorf("%s has no property %s", c.object, c.prop)

			continue
		}

		if got := flagOf(t, p); got&1 != 0 {
			t.Errorf("%s.%s is required (flags %d) but is optional in the API", c.object, c.prop, got)
		}
	}
}

// TestHoverDescriptions: a sample of properties carry non-empty descriptions
// (the IDE hover text).
func TestHoverDescriptions(t *testing.T) {
	t.Parallel()

	g := loadGenTypes(t)

	cases := []struct {
		object, prop string
	}{
		{"io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta", "labels"},
		{"io.k8s.api.apps.v1.DeploymentSpec", "replicas"},
		{"io.k8s.api.core.v1.ServiceSpec", "type"},
		{"io.k8s.api.core.v1.Container", "image"},
	}

	for _, c := range cases {
		obj, ok := namedObject(g.file, c.object)
		if !ok {
			t.Errorf("object %s not generated", c.object)

			continue
		}

		p := propsOf(t, obj)[c.prop]
		if desc, _ := p["description"].(string); desc == "" {
			t.Errorf("%s.%s has no description", c.object, c.prop)
		}
	}
}

// TestPermissiveFallbackBodies: kinds absent from the swagger keep the
// permissive body (metadata.name and metadata.generateName optional; metadata
// and spec open to any additional properties).
// resolveIndexRef follows an index.json reference ("types.json#/N") to the
// type entry.
func (g genTypes) resolveIndexRef(t *testing.T, ref any) map[string]any {
	t.Helper()

	m, ok := ref.(map[string]any)
	if !ok {
		t.Fatalf("index ref is not an object: %v", ref)
	}

	ptr, _ := m["$ref"].(string)

	return g.resolve(t, map[string]any{"$ref": strings.TrimPrefix(ptr, typesFileName)})
}

// TestAllCataloguedBodiesTyped: every catalogued resource has a CLOSED body
// type — no additionalProperties at the body level and none on the spec
// sub-object. Every catalogued kind is currently typed from the core
// swagger or the committed CRD schemas (swagger/crd/, merged in by
// cmd/gentypes), so no permissive fallback bodies exist. If a kind added to
// k8s/kinds.go lacks a schema in both sources, this test fails on purpose:
// supply the schema (scripts/update-gateway-api.sh /
// update-snapshotter.sh) or relax the assertion with a reason.
func TestAllCataloguedBodiesTyped(t *testing.T) {
	t.Parallel()

	g := loadGenTypes(t)

	resources, ok := g.index["resources"].(map[string]any)
	if !ok || len(resources) == 0 {
		t.Fatal("index.json has no resources")
	}

	for _, ref := range resources {
		resource := g.resolveIndexRef(t, ref)
		body := g.resolveIndexRef(t, resource["body"])

		if addl := body["additionalProperties"]; addl != nil {
			t.Errorf("body of %s is open to arbitrary properties: %v", body[nameKey], addl)
		}

		if spec, ok := propsOf(t, body)["spec"]; ok {
			specType := g.resolve(t, spec["type"])

			if addl := specType["additionalProperties"]; addl != nil {
				t.Errorf("spec of %s is open to arbitrary properties: %v", body[nameKey], addl)
			}
		}
	}

	// The fallback resource still exists for un-catalogued kinds.
	for _, entry := range g.file {
		if entry["$type"] == "ResourceType" && entry[nameKey] == "AnyKubernetesManifest" {
			return
		}
	}

	t.Error("fallback resource AnyKubernetesManifest missing")
}

func namedObject(file []map[string]any, name string) (map[string]any, bool) {
	for _, entry := range file {
		if entry["$type"] == "ObjectType" && entry[nameKey] == name {
			return entry, true
		}
	}

	return nil, false
}
