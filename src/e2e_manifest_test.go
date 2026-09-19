// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestExampleManifestsReparses checks the generated example manifests
// (run `bicep local-deploy dev.bicepparam` in ../samples/bicep/hello and
// ../samples/bicep/raw first — the output lands in
// ../samples/manifests/{hello,raw}/dev/combined.yaml) parse
// back with correct types: the property kubectl's YAML 1.1 parser depends
// on. The full example set lives in ../bicep-ext-k8smanifest-examples (it is not
// walked here; validate it with its scripts/validate-manifests.sh).
func TestExampleManifestsReparses(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "samples", "manifests")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skip("example manifests not generated yet (run bicep local-deploy dev.bicepparam in ../samples/bicep/hello)")
	}

	checked := 0

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || !strings.HasSuffix(d.Name(), ".yaml") {
			return nil
		}

		b, err := os.ReadFile(path) //nolint:gosec // path built from the fixed test fixture root
		if err != nil {
			return err
		}

		checkDocs(t, path, b)

		checked++

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if checked == 0 {
		t.Skip("no example manifests found (run bicep local-deploy dev.bicepparam in ../samples/bicep/hello)")
	}
}

// checkDocs parses every YAML document in b (files may be multi-document in
// combined mode) and requires each to carry apiVersion and kind. It also
// asserts that numeric-looking fields stay numbers on re-parse (a quoted
// "2" would come back as a string and break the Deployment controller).
func checkDocs(t *testing.T, name string, b []byte) {
	t.Helper()

	dec := yaml.NewDecoder(bytes.NewReader(b))
	docs := 0

	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}

		docs++

		rel := ""

		if r, err := filepath.Rel(filepath.Join("..", "samples", "manifests"), name); err == nil {
			rel = r
		}

		if doc["apiVersion"] == nil || doc["kind"] == nil {
			t.Errorf("%s: doc %d missing apiVersion/kind", rel, docs)

			continue
		}

		kind, _ := doc["kind"].(string)
		if kind == "Deployment" {
			spec, _ := doc["spec"].(map[string]any)

			if replicas, ok := spec["replicas"]; ok {
				if _, isInt := replicas.(int); !isInt {
					t.Errorf("%s: deployment %v replicas re-parsed as %T, want int", rel, doc["metadata"], replicas)
				}
			}
		}
	}

	if docs == 0 {
		t.Errorf("%s: no YAML documents found", name)
	}
}
