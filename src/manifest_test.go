// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These kind/type literals repeat across the render and combined-file
// tests; naming them keeps goconst (min-occurrences 4) quiet without
// restructuring the test tables.
const (
	deploymentKind = "Deployment"
	namespaceType  = "core/Namespace"
	configMapType  = "core/ConfigMap"
	configMapKind  = "ConfigMap"
	apiVersionKey  = "apiVersion"
	kindKey        = "kind"
	metadataKey    = "metadata"
	nameKey        = "name"
	outDir         = "out" // extension-config outputDir used by the write tests
)

func TestComputeManifestDeployment(t *testing.T) {
	t.Parallel()

	req := &resourceRequest{
		Type:       "apps/Deployment",
		APIVersion: "v1",
		Properties: `{"metadata":{"name":"web","namespace":"apps","labels":{"app":"web"}},"spec":{"replicas":2}}`,
		config:     &extensionConfig{OutputDir: "manifests"},
	}

	result, err := computeManifest(req)
	if err != nil {
		t.Fatalf("computeManifest: %v", err)
	}

	if result.meta.apiVersion != "apps/v1" {
		t.Errorf("apiVersion = %q, want apps/v1", result.meta.apiVersion)
	}

	if result.meta.kind != "Deployment" {
		t.Errorf("kind = %q, want Deployment", result.meta.kind)
	}

	if result.fileName != "apps-deployment-apps-web.yaml" {
		t.Errorf("fileName = %q, want apps-deployment-apps-web.yaml (group-kind-namespace-name)", result.fileName)
	}

	wantLines := []string{"apiVersion: apps/v1", "kind: Deployment", "metadata:"}
	for _, l := range wantLines {
		if !strings.Contains(result.yaml, l) {
			t.Errorf("yaml missing %q:\n%s", l, result.yaml)
		}
	}
	// apiVersion must come before kind, kind before metadata
	av := strings.Index(result.yaml, apiVersionKey)
	kind := strings.Index(result.yaml, "kind")

	md := strings.Index(result.yaml, metadataKey)
	if av >= kind || kind > md {
		t.Errorf("unexpected key order:\n%s", result.yaml)
	}
}

func TestComputeManifestCoreService(t *testing.T) {
	t.Parallel()

	req := &resourceRequest{
		Type:       "core/Service",
		APIVersion: "v1",
		Properties: `{"metadata":{"name":"web","namespace":"apps"},"spec":{"type":"ClusterIP","ports":[{"port":80,"targetPort":"http"}]}}`,
		config:     &extensionConfig{},
	}

	result, err := computeManifest(req)
	if err != nil {
		t.Fatalf("computeManifest: %v", err)
	}

	if result.meta.apiVersion != "v1" {
		t.Errorf("apiVersion = %q, want v1", result.meta.apiVersion)
	}

	if result.fileName != "service-apps-web.yaml" {
		t.Errorf("fileName = %q, want service-apps-web.yaml", result.fileName)
	}

	if !strings.Contains(result.yaml, "apiVersion: v1\n") {
		t.Errorf("yaml missing 'apiVersion: v1':\n%s", result.yaml)
	}
}

func TestComputeManifestFallbackCRD(t *testing.T) {
	t.Parallel()

	req := &resourceRequest{
		Type:       "example.com/MyWidget",
		APIVersion: "v1alpha1",
		Properties: `{"metadata":{"name":"w1"},"spec":{"count":3}}`,
		config:     &extensionConfig{OutputDir: outDir},
	}

	result, err := computeManifest(req)
	if err != nil {
		t.Fatalf("computeManifest: %v", err)
	}

	if result.meta.apiVersion != "example.com/v1alpha1" {
		t.Errorf("apiVersion = %q, want example.com/v1alpha1", result.meta.apiVersion)
	}

	if result.fileName != "example.com-my-widget-w1.yaml" {
		t.Errorf("fileName = %q, want example.com-my-widget-w1.yaml", result.fileName)
	}
}

// TestComputeManifestRaw verifies the k8smanifest/Raw verbatim passthrough:
// the body is { document: <manifest> } (Bicep resource bodies must be
// object literals, so a loadYamlContent'd manifest enters through the
// document property) and the wrapped document is emitted as-is —
// apiVersion/kind from the document, arbitrary extra top-level properties
// (here the sops metadata block of a SOPS-encrypted Secret) preserved.
func TestComputeManifestRaw(t *testing.T) {
	t.Parallel()

	req := &resourceRequest{
		Type:       "k8smanifest/Raw",
		APIVersion: "v1",
		Properties: `{"document":{"apiVersion":"v1","kind":"Secret","metadata":{"name":"ws","namespace":"ns1"},"stringData":{"TOK":"ENC[AES256_GCM,data:xyz,iv:abc,tag:def,type:str]"},"sops":{"mac":"ENC[AES256_GCM,data:mac,iv:m2c,tag:mae,type:str]","version":"3.13.3"}}}`,
		config:     &extensionConfig{OutputDir: outDir},
	}

	result, err := computeManifest(req)
	if err != nil {
		t.Fatalf("computeManifest: %v", err)
	}

	// Metadata derives from the wrapped document, not the type string: the
	// document kind drives the file name (like a real Secret) and the
	// deploy priority.
	if result.meta.kind != "Secret" || result.meta.apiVersion != "v1" {
		t.Errorf("meta = %s/%s, want Secret/v1 (from the document)", result.meta.kind, result.meta.apiVersion)
	}

	if result.meta.known {
		t.Error("a raw document must not take the catalogue-kind path (scope check, typed body)")
	}

	if result.fileName != "secret-ns1-ws.yaml" {
		t.Errorf("fileName = %q, want secret-ns1-ws.yaml (the document kind, not Raw)", result.fileName)
	}

	// The rendered document keeps apiVersion/kind from the document and
	// carries the sops block + ENC values verbatim (sops decrypts on the
	// values, so re-serialisation must not mangle the ENC strings).
	for _, want := range []string{"apiVersion: v1", "kind: Secret", "mac: ENC[AES256_GCM,data:mac,iv:m2c,tag:mae,type:str]", "TOK: ENC[AES256_GCM,data:xyz,iv:abc,tag:def,type:str]"} {
		if !strings.Contains(result.yaml, want) {
			t.Errorf("manifest missing %q:\n%s", want, result.yaml)
		}
	}

	// The wrapper property must not leak into the emitted manifest.
	if strings.Contains(result.yaml, "document") {
		t.Errorf("the document wrapper leaked into the manifest:\n%s", result.yaml)
	}
}

// TestComputeManifestRawMalformedBody: the wrapper shape is strict — the
// body is exactly { document: <manifest> } and the document carries
// apiVersion/kind. Anything else fails with an actionable error rather
// than emitting a broken document.
func TestComputeManifestRawMalformedBody(t *testing.T) {
	t.Parallel()

	for label, props := range map[string]string{
		"missing kind in document":       `{"document":{"apiVersion":"v1","metadata":{"name":"ws"}}}`,
		"missing apiVersion in document": `{"document":{"kind":"Secret","metadata":{"name":"ws"}}}`,
		"missing document property":      `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"ws"}}`,
		"extra property beside document": `{"document":{"apiVersion":"v1","kind":"Secret","metadata":{"name":"ws"}},"stray":1}`,
		"document is not an object":      `{"document":"not-a-manifest"}`,
	} {
		req := &resourceRequest{
			Type:       "k8smanifest/Raw",
			APIVersion: "v1",
			Properties: props,
			config:     &extensionConfig{OutputDir: t.TempDir()},
		}

		if _, err := computeManifest(req); err == nil || !strings.Contains(err.Error(), "k8smanifest/Raw") {
			t.Errorf("%s: error = %v, want an error naming the Raw body shape", label, err)
		}
	}
}

// TestWriteManifestCombinedRawOrder: a raw document sorts in the combined
// file by its BODY kind (a raw Secret before the Deployment it feeds), not
// by the Raw passthrough type (which would land in the unknown-kinds tail).
func TestWriteManifestCombinedRawOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	kinds := writeCombinedOrder(t, dir, "apps/Deployment", "web",
		`{"metadata":{"name":"web","namespace":"ns1"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"web"}},"template":{"metadata":{"labels":{"app":"web"}},"spec":{"containers":[{"name":"c","image":"nginx"}]}}}}`)
	kinds = writeCombinedOrder(t, dir, "k8smanifest/Raw", "web-secret",
		`{"document":{"apiVersion":"v1","kind":"Secret","metadata":{"name":"web-secret","namespace":"ns1"},"data":{"k":"ENC[AES256_GCM,data:xyz,iv:abc,tag:def,type:str]"},"sops":{"mac":"ENC[AES256_GCM,data:mac,iv:m2c,tag:mae,type:str"}}}`)

	want := []string{"Secret", deploymentKind}
	if len(kinds) != len(want) {
		t.Fatalf("document order = %v, want %v", kinds, want)
	}

	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("document order = %v, want %v", kinds, want)
		}
	}
}

// TestComputeManifestDropsNullProps verifies that null-valued properties
// (Bicep's way of expressing "absent") are stripped from the manifest
// before rendering — Kubernetes treats absent and null differently, and
// CRD validation rejects null where absence is expected.
func TestComputeManifestDropsNullProps(t *testing.T) {
	t.Parallel()

	req := &resourceRequest{
		Type:       "networking.k8s.io/NetworkPolicy@v1",
		APIVersion: "v1",
		Properties: `{"metadata":{"name":"np1","namespace":"ns1"},"spec":{"ingress":[{"from":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"a"}},"podSelector":null}],"ports":[{"protocol":null,"port":80}]}]}}`,
		config:     &extensionConfig{OutputDir: outDir},
	}

	result, err := computeManifest(req)
	if err != nil {
		t.Fatalf("computeManifest: %v", err)
	}

	for _, absent := range []string{"podSelector", "protocol", "null"} {
		if strings.Contains(result.yaml, absent) {
			t.Errorf("yaml still contains %q:\n%s", absent, result.yaml)
		}
	}

	// Sanity: the non-null siblings survived.
	for _, present := range []string{"namespaceSelector", "port: 80"} {
		if !strings.Contains(result.yaml, present) {
			t.Errorf("yaml missing %q:\n%s", present, result.yaml)
		}
	}
}

func TestComputeManifestRequiresName(t *testing.T) {
	t.Parallel()

	req := &resourceRequest{
		Type:       "apps/Deployment",
		APIVersion: "v1",
		Properties: `{"metadata":{"namespace":"apps"},"spec":{"replicas":1}}`,
		config:     &extensionConfig{},
	}
	if _, err := computeManifest(req); err == nil {
		t.Fatal("expected an error when metadata.name is missing")
	}
}

func TestWriteManifest(t *testing.T) {
	t.Parallel()

	req := &resourceRequest{
		Type:       namespaceType,
		APIVersion: "v1",
		Properties: `{"metadata":{"name":"bicep-poc"}}`,
		config:     &extensionConfig{},
	}

	result, err := computeManifest(req)
	if err != nil {
		t.Fatalf("computeManifest: %v", err)
	}

	dir := t.TempDir()

	path, err := writeManifest(&extensionConfig{OutputDir: dir}, result)
	if err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	if !strings.HasSuffix(path, "namespace-bicep-poc.yaml") {
		t.Errorf("path = %q, want suffix namespace-bicep-poc.yaml", path)
	}
}

func TestRenderYAMLScalars(t *testing.T) {
	t.Parallel()

	s, err := renderYAML(map[string]any{
		apiVersionKey: "v1",
		kindKey:       configMapKind,
		metadataKey:   map[string]any{"name": "cm"},
		"data":        map[string]any{"true": "yes", "number": "1", "empty": "", "yes": "y1", "on": "y2", "off": "y3"},
	})
	if err != nil {
		t.Fatalf("renderYAML: %v", err)
	}
	// The value "yes" must be quoted so it is not parsed as a boolean.
	if !strings.Contains(s, `"yes"`) {
		t.Errorf("expected quoted \"yes\" in:\n%s", s)
	}
	// The key "true" must be quoted so it is not parsed as a boolean key.
	if !strings.Contains(s, "\"true\":") {
		t.Errorf("expected quoted \"true\" key in:\n%s", s)
	}

	// YAML 1.1 boolean-token keys (ConfigMap data keys) must be quoted too;
	// yaml.v3 (1.2) would not quote them, so the keys are the regression.
	for _, k := range []string{"yes", "on", "off"} {
		if !strings.Contains(s, "\""+k+"\""+":") {
			t.Errorf("expected quoted %q key in:\n%s", k, s)
		}
	}

	// Round-trip: keys must survive a parse as strings, not booleans.
	var reparse map[string]any

	if err := yaml.Unmarshal([]byte(s), &reparse); err != nil {
		t.Fatalf("round-trip parse failed: %v", err)
	}

	data, _ := reparse["data"].(map[string]any)
	for k, v := range data {
		if _, isBool := v.(bool); isBool {
			t.Errorf("key %q came back as a boolean on re-parse (YAML 1.1 misread):\n%s", k, s)
		}
	}
}

func TestRenderYAMLNnumbers(t *testing.T) {
	t.Parallel()

	s, err := renderYAML(map[string]any{
		apiVersionKey: "apps/v1",
		kindKey:       deploymentKind,
		metadataKey:   map[string]any{"name": "d"},
		"spec":        map[string]any{"replicas": float64(2), "threshold": 0.5},
	})
	if err != nil {
		t.Fatalf("renderYAML: %v", err)
	}

	if !strings.Contains(s, "replicas: 2\n") {
		t.Errorf("expected unquoted int 'replicas: 2' in:\n%s", s)
	}

	if strings.Contains(s, `replicas: "2"`) {
		t.Errorf("replicas must not be a quoted string:\n%s", s)
	}

	if !strings.Contains(s, "threshold: 0.5") {
		t.Errorf("expected 'threshold: 0.5' in:\n%s", s)
	}
}

func TestWriteManifestCombined(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &extensionConfig{OutputDir: dir, Combine: true}

	r1 := &resourceRequest{
		Type:       namespaceType,
		APIVersion: "v1",
		Properties: `{"metadata":{"name":"bicep-poc"}}`,
		config:     cfg,
	}

	res1, err := computeManifest(r1)
	if err != nil {
		t.Fatalf("computeManifest: %v", err)
	}

	if _, err := writeManifest(cfg, res1); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	r2 := &resourceRequest{
		Type:       "core/Service",
		APIVersion: "v1",
		Properties: `{"metadata":{"name":"web","namespace":"bicep-poc"},"spec":{"ports":[{"port":80}]}}`,
		config:     cfg,
	}

	res2, err := computeManifest(r2)
	if err != nil {
		t.Fatalf("computeManifest: %v", err)
	}

	path, err := writeManifest(cfg, res2)
	if err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	if filepath.Base(path) != "combined.yaml" {
		t.Errorf("path = %q, want combined.yaml", path)
	}

	b, err := os.ReadFile(path) //nolint:gosec // temp test dir
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Count(string(b), "---\n"); got != 1 {
		t.Errorf("separator count = %d, want 1:\n%s", got, b)
	}

	dec := yaml.NewDecoder(bytes.NewReader(b))
	docs := 0

	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}

		docs++
	}

	if docs != 2 {
		t.Errorf("documents = %d, want 2", docs)
	}
}

// writeCombined writes a single resource (kind, type name, properties) into
// the combined file at dir and returns the written file's document kinds in
// order.
func writeCombinedOrder(t *testing.T, dir, typ, name, props string) []string {
	t.Helper()

	cfg := &extensionConfig{OutputDir: dir, Combine: true}
	req := &resourceRequest{Type: typ, APIVersion: "v1", Properties: props, config: cfg}

	res, err := computeManifest(req)
	if err != nil {
		t.Fatalf("computeManifest(%s/%s): %v", typ, name, err)
	}

	if _, err := writeManifest(cfg, res); err != nil {
		t.Fatalf("writeManifest(%s/%s): %v", typ, name, err)
	}

	b, err := os.ReadFile(filepath.Join(dir, combinedFileName)) //nolint:gosec // temp test dir
	if err != nil {
		t.Fatal(err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(b))

	var kinds []string

	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}

		if k, ok := doc["kind"].(string); ok {
			kinds = append(kinds, k)
		}
	}

	return kinds
}

// TestWriteManifestCombinedSameNameAcrossNamespaces guards against
// same-named policies in different namespaces collapsing into one document:
// the dedupe key must be (kind, namespace, name), because the internal
// network-policy module emits identically-named policies per tenant
// namespace in a single run.
func TestWriteManifestCombinedSameNameAcrossNamespaces(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &extensionConfig{OutputDir: dir, Combine: true}

	for _, ns := range []string{"d1-z01-web", "d1-z02-web"} {
		r := &resourceRequest{
			Type:       "networking.k8s.io/NetworkPolicy@v1",
			APIVersion: "v1",
			Properties: fmt.Sprintf(`{"metadata":{"name":"web-allow-http","namespace":%q},"spec":{"podSelector":{},"policyTypes":["Ingress"]}}`, ns),
			config:     cfg,
		}

		res, err := computeManifest(r)
		if err != nil {
			t.Fatalf("computeManifest(%s): %v", ns, err)
		}

		if _, err := writeManifest(cfg, res); err != nil {
			t.Fatalf("writeManifest(%s): %v", ns, err)
		}
	}

	b, err := os.ReadFile(filepath.Join(dir, "combined.yaml")) //nolint:gosec // temp test dir
	if err != nil {
		t.Fatal(err)
	}

	s := string(b)
	for _, ns := range []string{"d1-z01-web", "d1-z02-web"} {
		if !strings.Contains(s, "namespace: "+ns) {
			t.Errorf("combined.yaml missing namespace %q:\n%s", ns, s)
		}
	}

	// A re-invocation for the same (kind, namespace, name) must replace,
	// not duplicate.
	r := &resourceRequest{
		Type:       "networking.k8s.io/NetworkPolicy@v1",
		APIVersion: "v1",
		Properties: `{"metadata":{"name":"web-allow-http","namespace":"d1-z01-web"},"spec":{"podSelector":{},"policyTypes":["Ingress"]}}`,
		config:     cfg,
	}

	res, err := computeManifest(r)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := writeManifest(cfg, res); err != nil {
		t.Fatal(err)
	}

	b, err = os.ReadFile(filepath.Join(dir, "combined.yaml")) //nolint:gosec // temp test dir
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Count(string(b), "name: web-allow-http"); got != 2 {
		t.Errorf("name count = %d, want 2 (one per namespace):\n%s", got, b)
	}

	// Same kind + name in different namespaces must sort by namespace
	// (deterministic, insertion-order-independent).
	if strings.Index(s, "namespace: d1-z01-web") > strings.Index(s, "namespace: d1-z02-web") {
		t.Errorf("d1-z01-web doc should sort before d1-z02-web:\n%s", s)
	}
}

func TestWriteManifestCombinedOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Written in deliberately unhelpful order (workload, scaler, config,
	// namespace last); the combined file must come out in deploy order:
	// namespace, config, workload, scaler.
	writeCombinedOrder(t, dir, "apps/Deployment", "web",
		`{"metadata":{"name":"web","namespace":"ns1"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"web"}},"template":{"metadata":{"labels":{"app":"web"}},"spec":{"containers":[{"name":"c","image":"nginx"}]}}}}`)
	writeCombinedOrder(t, dir, "autoscaling/HorizontalPodAutoscaler", "web-hpa",
		fmt.Sprintf(`{"metadata":{"name":"web-hpa","namespace":"ns1"},"spec":{"scaleTargetRef":{"apiVersion":"apps/v1","kind":"%s","name":"web"},"maxReplicas":2}}`, deploymentKind))
	writeCombinedOrder(t, dir, "core/ConfigMap", "web-config",
		`{"metadata":{"name":"web-config","namespace":"ns1"},"data":{"k":"v"}}`)
	kinds := writeCombinedOrder(t, dir, namespaceType, "ns1",
		`{"metadata":{"name":"ns1"}}`)

	want := []string{"Namespace", configMapKind, deploymentKind, "HorizontalPodAutoscaler"}
	if len(kinds) != len(want) {
		t.Fatalf("document order = %v, want %v", kinds, want)
	}

	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("document order = %v, want %v", kinds, want)
		}
	}

	// Re-invoking for the same resource replaces its document, it does not
	// duplicate it.
	kinds = writeCombinedOrder(t, dir, namespaceType, "ns1",
		`{"metadata":{"name":"ns1"}}`)
	if len(kinds) != len(want) {
		t.Fatalf("document count after re-invocation = %d (kinds %v), want %d", len(kinds), kinds, len(want))
	}
}

func TestDeployPriority(t *testing.T) {
	t.Parallel()

	// The relations that make a combined file kubectl-safe in one pass.
	relations := [][2]string{
		{"Namespace", "ServiceAccount"},
		{"Namespace", deploymentKind},
		{configMapKind, deploymentKind},
		{"Secret", "StatefulSet"},
		{"ServiceAccount", "CronJob"},
		{"Role", "RoleBinding"},
		{"Service", "Ingress"},
		{"Gateway", "HTTPRoute"},
		{deploymentKind, "HorizontalPodAutoscaler"},
		{"CustomResourceDefinition", "MyWidget"},
	}

	for _, r := range relations {
		if deployPriority(r[0]) > deployPriority(r[1]) {
			t.Errorf("deployPriority(%s) = %d not before deployPriority(%s) = %d", r[0], deployPriority(r[0]), r[1], deployPriority(r[1]))
		}
	}
}

// TestSingleFileSameNameAcrossNamespaces guards Issue 1: two same-named
// resources in different namespaces must not write the same file in
// single-file mode.
func TestSingleFileSameNameAcrossNamespaces(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	for _, ns := range []string{"z01", "z02"} {
		cfg := &extensionConfig{OutputDir: dir}
		r := &resourceRequest{
			Type:       configMapType,
			APIVersion: "v1",
			Properties: fmt.Sprintf(`{"metadata":{"name":"app-config","namespace":%q},"data":{"k":"v"}}`, ns),
			config:     cfg,
		}

		res, err := computeManifest(r)
		if err != nil {
			t.Fatalf("computeManifest(%s): %v", ns, err)
		}

		if _, err := writeManifest(cfg, res); err != nil {
			t.Fatalf("writeManifest(%s): %v", ns, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 2 {
		t.Fatalf("want 2 files (one per namespace), got %d — one namespace was silently overwritten", len(entries))
	}
}

// TestCombinedBufferDirNormalisation guards Issue 4: different spellings of
// the same output directory must share one combined-file buffer.
func TestCombinedBufferDirNormalisation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "out")

	mk := func(props string) *manifestResult {
		t.Helper()

		r := &resourceRequest{Type: namespaceType, APIVersion: "v1", Properties: props}

		res, err := computeManifest(r)
		if err != nil {
			t.Fatal(err)
		}

		return res
	}

	if _, err := writeManifest(&extensionConfig{OutputDir: out, Combine: true}, mk(`{"metadata":{"name":"ns1"}}`)); err != nil {
		t.Fatal(err)
	}
	// Same directory, trailing slash.
	if _, err := writeManifest(&extensionConfig{OutputDir: out + "/", Combine: true}, mk(`{"metadata":{"name":"ns2"}}`)); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(out, combinedFileName)) //nolint:gosec // path is a fixed temp test dir
	if err != nil {
		t.Fatal(err)
	}

	s := string(b)

	if !strings.Contains(s, "name: ns1") || !strings.Contains(s, "name: ns2") {
		t.Errorf("combined.yaml lost a document (buffer keyed by un-normalised dir):\n%s", s)
	}
}

// TestRenderYAMLExponentFloat guards Issue 8: an exponent float must render
// with a mantissa dot so YAML 1.1 (kubectl) keeps it a number.
func TestRenderYAMLExponentFloat(t *testing.T) {
	t.Parallel()

	s, err := renderYAML(map[string]any{
		apiVersionKey: "v1",
		kindKey:       configMapKind,
		metadataKey:   map[string]any{"name": "cm"},
		"data":        map[string]any{"rate": 0.0000001, "big": 1.5e21},
	})
	if err != nil {
		t.Fatalf("renderYAML: %v", err)
	}

	if strings.Contains(s, "1e-07") {
		t.Errorf("exponent float without a mantissa dot is a YAML 1.1 string:\n%s", s)
	}

	var reparse map[string]any

	if err := yaml.Unmarshal([]byte(s), &reparse); err != nil {
		t.Fatal(err)
	}

	data, _ := reparse["data"].(map[string]any)
	if v, ok := data["rate"].(float64); !ok || v != 0.0000001 {
		t.Errorf("rate re-parsed as %v (%T), want float 0.0000001", data["rate"], data["rate"])
	}

	if v, ok := data["big"].(float64); !ok || v != 1.5e21 {
		t.Errorf("big re-parsed as %v (%T), want float 1.5e21", data["big"], data["big"])
	}
}

// TestScopeValidation guards Issue 5.6: the catalogue's Namespaced flag
// drives metadata.namespace validation for known kinds; fallback (CRD)
// kinds are not validated (their scope is unknown).
func TestScopeValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		bicepType string
		props     string
		wantErr   bool
	}{
		{"namespaced kind without namespace", "core/ConfigMap", `{"metadata":{"name":"cm"},"data":{"k":"v"}}`, true},
		{"namespaced kind with namespace", "core/ConfigMap", `{"metadata":{"name":"cm","namespace":"a"},"data":{"k":"v"}}`, false},
		{"cluster-scoped kind with namespace", namespaceType, `{"metadata":{"name":"ns","namespace":"a"}}`, true},
		{"cluster-scoped kind without namespace", namespaceType, `{"metadata":{"name":"ns"}}`, false},
		{"unknown CRD skips the check", "example.com/MyWidget", `{"metadata":{"name":"w"}}`, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			r := &resourceRequest{Type: c.bicepType, APIVersion: "v1", Properties: c.props}
			_, err := computeManifest(r)

			if (err != nil) != c.wantErr {
				t.Errorf("computeManifest: err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}

// TestCombinedNamelessDisambiguation guards Issue 9: distinct nameless
// documents (the lenient kinds default to "unnamed") must both survive in
// the combined file, while a re-invocation of the same nameless document
// still dedupes.
func TestCombinedNamelessDisambiguation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &extensionConfig{OutputDir: dir, Combine: true}

	write := func(bicepType, props string) {
		t.Helper()

		r := &resourceRequest{Type: bicepType, APIVersion: "v1", Properties: props}

		res, err := computeManifest(r)
		if err != nil {
			t.Fatalf("computeManifest: %v", err)
		}

		if _, err := writeManifest(cfg, res); err != nil {
			t.Fatalf("writeManifest: %v", err)
		}
	}

	first := `{"metadata":{"namespace":"a"},"reason":"r1","message":"m1"}`
	second := `{"metadata":{"namespace":"a"},"reason":"r2","message":"m2"}`

	write("core/Event", first)
	write("core/Event", second)
	write("core/Event", first) // re-invocation: must dedupe, not add a third

	b, err := os.ReadFile(filepath.Join(dir, combinedFileName)) //nolint:gosec // path is a fixed temp test dir
	if err != nil {
		t.Fatal(err)
	}

	s := string(b)

	if !strings.Contains(s, "r1") || !strings.Contains(s, "r2") {
		t.Errorf("both distinct nameless events must be present:\n%s", s)
	}

	if got := strings.Count(s, "---\n") + strings.Count(s, "kind: Event"); got != 3 {
		t.Errorf("want exactly 2 event documents (re-invocation deduped), got count hint %d:\n%s", got, s)
	}
}

// TestSingleFileFilenameCollision guards the silent-clobber bug: two
// distinct resources mapping to the same file must fail, not overwrite
// each other.
func TestSingleFileFilenameCollision(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	cfg := &extensionConfig{OutputDir: dir}

	mk := func(props string) *manifestResult {
		r := &resourceRequest{
			Type:       configMapType,
			APIVersion: "v1",
			Properties: props,
			config:     cfg,
		}

		res, err := computeManifest(r)
		if err != nil {
			t.Fatalf("computeManifest: %v", err)
		}

		return res
	}

	first := mk(`{"metadata":{"name":"app-config","namespace":"default"},"data":{"k":"v1"}}`)

	if _, err := writeManifest(cfg, first); err != nil {
		t.Fatalf("first writeManifest: %v", err)
	}

	// A DISTINCT resource with the same (namespace, name) identity cannot
	// exist in Kubernetes, but a re-deploy of a template with two resources
	// named "app-config" in one namespace maps both to the same file. The
	// collision is detected via the file-owner record: same kind/namespace
	// but the second write is a *different* resource document.
	second := mk(`{"metadata":{"name":"app-config","namespace":"default"},"data":{"k":"v2"}}`)

	if _, err := writeManifest(cfg, second); err == nil {
		t.Fatal("expected a collision error for two distinct resources mapping to the same file, got nil")
	}
}

// TestSingleFileFilenameCollisionSanitised guards the a_b / a-b collision:
// both names sanitize to the same slug and must not silently clobber.
func TestSingleFileFilenameCollisionSanitised(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &extensionConfig{OutputDir: dir}

	mk := func(name string) *manifestResult {
		r := &resourceRequest{
			Type:       configMapType,
			APIVersion: "v1",
			Properties: fmt.Sprintf(`{"metadata":{"name":%q,"namespace":"default"},"data":{"k":"v"}}`, name),
			config:     cfg,
		}

		res, err := computeManifest(r)
		if err != nil {
			t.Fatalf("computeManifest(%s): %v", name, err)
		}

		return res
	}

	first := mk("a_b")

	if _, err := writeManifest(cfg, first); err != nil {
		t.Fatalf("first writeManifest: %v", err)
	}

	// "a-b" is a distinct metadata.name that sanitizes to the same slug.
	second := mk("a-b")

	if _, err := writeManifest(cfg, second); err == nil {
		t.Fatal("expected a collision error for a_b / a-b (same sanitised slug), got nil")
	}
}

// TestSingleFileReinvocation guards the idempotent re-run contract: the
// same resource written twice with identical content must be a no-op, but
// a different document mapping onto the same file must fail (see the
// collision tests above).
func TestSingleFileReinvocation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &extensionConfig{OutputDir: dir}

	mk := func(props string) *manifestResult {
		r := &resourceRequest{
			Type:       configMapType,
			APIVersion: "v1",
			Properties: props,
			config:     cfg,
		}

		res, err := computeManifest(r)
		if err != nil {
			t.Fatalf("computeManifest: %v", err)
		}

		return res
	}

	first := mk(`{"metadata":{"name":"app-config","namespace":"default"},"data":{"k":"v1"}}`)

	if _, err := writeManifest(cfg, first); err != nil {
		t.Fatalf("first writeManifest: %v", err)
	}

	// Identical re-invocation: must not error, must not create a second file.
	if _, err := writeManifest(cfg, first); err != nil {
		t.Fatalf("identical re-invocation must not error, got %v", err)
	}

	// A different document mapping onto the same file: must error.
	changed := mk(`{"metadata":{"name":"app-config","namespace":"default"},"data":{"k":"v2"}}`)

	if _, err := writeManifest(cfg, changed); err == nil {
		t.Fatal("expected a collision error for a different document on the same file, got nil")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 {
		t.Fatalf("want exactly 1 file after re-invocation, got %d", len(entries))
	}
}

// TestSingleFileNamelessDisambiguation: nameless kinds (no metadata.name)
// share one file per (kind, namespace); distinct documents must be
// disambiguated (_2) rather than clobbered.
func TestSingleFileNamelessDisambiguation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &extensionConfig{OutputDir: dir}

	mk := func(reason string) *manifestResult {
		r := &resourceRequest{
			Type:       "core/Event",
			APIVersion: "v1",
			Properties: fmt.Sprintf(`{"metadata":{"namespace":"default"},"reason":%q}`, reason),
			config:     cfg,
		}

		res, err := computeManifest(r)
		if err != nil {
			t.Fatalf("computeManifest: %v", err)
		}

		return res
	}

	first := mk("r1")

	if _, err := writeManifest(cfg, first); err != nil {
		t.Fatalf("first writeManifest: %v", err)
	}

	second := mk("r2")

	if _, err := writeManifest(cfg, second); err != nil {
		t.Fatalf("second (distinct nameless) writeManifest must not error, got %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}

		t.Fatalf("want 2 files (distinct nameless events disambiguated), got %d: %v", len(entries), names)
	}

	for _, e := range entries {
		if !strings.Contains(e.Name(), "event") || !strings.HasSuffix(e.Name(), ".yaml") {
			t.Errorf("unexpected file name %q", e.Name())
		}
	}
}

// TestComputeManifestGenerateName guards generateName support: a resource
// with only metadata.generateName (no metadata.name) must compute cleanly
// and derive its file name from the prefix.
func TestComputeManifestGenerateName(t *testing.T) {
	t.Parallel()

	r := &resourceRequest{
		Type:       "batch/Job",
		APIVersion: "v1",
		Properties: `{"metadata":{"generateName":"probe-job-","namespace":"default"},"spec":{"template":{"spec":{"containers":[{"name":"c","image":"nginx:1.27-alpine"}]}}}}`,
		config:     &extensionConfig{},
	}

	res, err := computeManifest(r)
	if err != nil {
		t.Fatalf("computeManifest with generateName-only metadata: %v", err)
	}

	if res.rawName != "" {
		t.Errorf("rawName = %q, want empty (no explicit metadata.name)", res.rawName)
	}

	if res.name != "probe-job" {
		t.Errorf("name slug = %q, want probe-job", res.name)
	}

	if !strings.HasSuffix(res.fileName, "job-default-probe-job.yaml") {
		t.Errorf("fileName = %q, want suffix job-default-probe-job.yaml", res.fileName)
	}
}

// TestComputeManifestRequiresNameOrGenerateName: a named kind with neither
// metadata.name nor metadata.generateName must still be rejected.
func TestComputeManifestRequiresNameOrGenerateName(t *testing.T) {
	t.Parallel()

	r := &resourceRequest{
		Type:       "batch/Job",
		APIVersion: "v1",
		Properties: `{"metadata":{"namespace":"default"},"spec":{"template":{"spec":{"containers":[{"name":"c","image":"nginx:1.27-alpine"}]}}}}`,
		config:     &extensionConfig{},
	}

	if _, err := computeManifest(r); err == nil {
		t.Fatal("expected an error when both metadata.name and metadata.generateName are missing")
	}
}

func TestResultPath(t *testing.T) {
	t.Parallel()

	result := &manifestResult{fileName: "apps-deployment-apps-web.yaml"}

	if got := resultPath(&extensionConfig{OutputDir: outDir}, result); got != filepath.Join(outDir, "apps-deployment-apps-web.yaml") {
		t.Errorf("single-file path = %q, want out/apps-deployment-apps-web.yaml", got)
	}

	if got := resultPath(&extensionConfig{OutputDir: outDir, Combine: true}, result); got != filepath.Join(outDir, combinedFileName) {
		t.Errorf("combined path = %q, want out/combined.yaml (the per-kind fileName must be ignored)", got)
	}

	if got := resultPath(&extensionConfig{}, result); got != filepath.Join(defaultOutputDir, "apps-deployment-apps-web.yaml") {
		t.Errorf("default-dir path = %q, want manifests/apps-deployment-apps-web.yaml", got)
	}
}

// TestHasLeadingIndicator pins the indicator rule: special only as the first
// character ("a#b" is plain, "#anchor" is not; a leading -/:/? only when
// alone or followed by a space).
func TestHasLeadingIndicator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		s    string
		want bool
	}{
		{"#anchor", true},
		{"!tag", true},
		{"*alias", true},
		{"&anchor", true},
		{"|block", true},
		{">folded", true},
		{"@at", true},
		{"`tick", true},
		{"?", true},
		{"? ", true},
		{"?abc", true}, // '?' is an indicator character itself: special whatever follows
		{"-", true},
		{"- ", true},
		{":", true},
		{": ", true},
		{"plain", false},
		{"a#b", false},
		{"-abc", false},
		{":-", false},
		{":abc", false},
	}

	for _, tc := range cases {
		if got := hasLeadingIndicator(tc.s); got != tc.want {
			t.Errorf("hasLeadingIndicator(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}
