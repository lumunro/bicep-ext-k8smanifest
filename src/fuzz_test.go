// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

// Fuzz tests for the deterministic-output contract: rendered YAML must be
// stable across render -> parse -> re-render round trips, and the filename
// slugger must be a total, idempotent rune mapping. `go test` runs the seed
// corpus (fast); `go test -fuzz` runs the real fuzzer locally.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// yamlToAny rebuilds the map[string]any shape renderYAML consumes from a
// parsed document, restoring exactly the types a manifest body flows in as
// (json.Unmarshal output): strings, bools, nil, and every number as float64.
func yamlToAny(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)

		for i := 0; i < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]

			if k.Kind != yaml.ScalarNode {
				return nil, errors.New("non-scalar map key in rendered YAML")
			}

			val, err := yamlToAny(v)
			if err != nil {
				return nil, err
			}

			m[k.Value] = val
		}

		return m, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))

		for _, item := range n.Content {
			val, err := yamlToAny(item)
			if err != nil {
				return nil, err
			}

			out = append(out, val)
		}

		return out, nil
	case yaml.ScalarNode:
		return scalarValue(n)
	case yaml.DocumentNode, yaml.AliasNode:
		return nil, errors.New("unexpected document/alias node in rendered YAML")
	default:
		return nil, fmt.Errorf("unexpected node kind %v", n.Kind)
	}
}

// scalarValue converts one rendered scalar to the Go type the renderer
// consumes (json.Unmarshal semantics: every number is a float64 — integral
// float64s carry tag !!int, so !!int is read back as a float64 too).
func scalarValue(n *yaml.Node) (any, error) {
	switch n.Tag {
	case "!!str":
		return n.Value, nil
	case "!!bool":
		b, err := strconv.ParseBool(n.Value)
		if err != nil {
			return nil, fmt.Errorf("bool scalar %q: %w", n.Value, err)
		}

		return b, nil
	case "!!int", "!!float":
		f, err := strconv.ParseFloat(n.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("number scalar %q: %w", n.Value, err)
		}

		return f, nil
	case "!!null":
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected tag %q in rendered YAML (value %q)", n.Tag, n.Value)
	}
}

// FuzzRenderYAMLRoundTrip asserts renderYAML is idempotent: rendering a JSON
// document, re-parsing the YAML and rendering again must be byte-identical.
// A violation means the output drifts on re-deploy — the contract Flux syncs
// against. (YAML 1.1 reader semantics are additionally covered by the e2e
// re-parse test against the committed example manifests.)
func FuzzRenderYAMLRoundTrip(f *testing.F) {
	// Seed corpus: the shapes that have bitten us — YAML 1.1 boolean/null/
	// number tokens as keys and values, exponent floats, integral floats,
	// nested lists and maps.
	f.Add(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"yes","namespace":"on","labels":{"off":"Off","true":"TRUE","~":"null"}},"data":{"yes":"true","10":"0x10","0.5":"1e-07"},"spec":{"replicas":2,"scale":1.5,"epsilon":1e-07,"big":6.02e23,"threshold":1e+15}}`)
	f.Add(`{"metadata":{"name":"web"},"spec":{"containers":[{"name":"web","resources":{"limits":{"cpu":"0.01","memory":"128Mi"}},"env":[{"name":"A","value":"b"}]}]}}`)
	f.Add(`{}`)
	f.Add(`{"a":-0.5,"b":[1,2.5,"3"],"empty":null}`)

	f.Fuzz(func(t *testing.T, in string) {
		var doc map[string]any

		if err := json.Unmarshal([]byte(in), &doc); err != nil {
			return // only JSON objects exercise the renderer
		}

		first, err := renderYAML(doc)
		if err != nil {
			t.Fatalf("renderYAML: %v", err)
		}

		var node yaml.Node

		if err := yaml.Unmarshal([]byte(first), &node); err != nil {
			t.Fatalf("re-parse rendered YAML: %v\n%s", err, first)
		}

		// Unwrap the document node: Unmarshal yields the root mapping as
		// DocumentNode.Content[0].
		if node.Kind == yaml.DocumentNode && len(node.Content) != 1 {
			t.Fatalf("unexpected document shape:\n%s", first)
		}

		if node.Kind == yaml.DocumentNode {
			node = *node.Content[0]
		}

		rebuilt, err := yamlToAny(&node)
		if err != nil {
			t.Fatalf("rebuild from rendered YAML: %v\n%s", err, first)
		}

		m, ok := rebuilt.(map[string]any)
		if !ok {
			t.Fatalf("rebuilt document is %T, want map[string]any\n%s", rebuilt, first)
		}

		second, err := renderYAML(m)
		if err != nil {
			t.Fatalf("re-render: %v", err)
		}

		if first != second {
			t.Fatalf("render is not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
		}
	})
}

// FuzzSanitizeName asserts the filename slugger is a total, idempotent
// mapping into the safe character set (a collision or an empty slug would
// break the single-file collision guard and the file naming contract).
func FuzzSanitizeName(f *testing.F) {
	f.Add("acme-web")
	f.Add("Acme.Web_V1")
	f.Add("a--b..c")
	f.Add("acmé-café")
	f.Add(strings.Repeat("x", 100))

	f.Fuzz(func(t *testing.T, in string) {
		got := sanitizeName(in)

		// Each input rune maps to exactly one output rune (byte length may
		// shrink: a multi-byte rune like "é" collapses to one "-").
		if utf8.RuneCountInString(got) != utf8.RuneCountInString(in) {
			t.Fatalf("rune count changed: %d -> %d", utf8.RuneCountInString(in), utf8.RuneCountInString(got))
		}

		for _, r := range got {
			if !isSlugRune(r) {
				t.Fatalf("invalid slug rune %q in %q", r, got)
			}
		}

		if redo := sanitizeName(got); redo != got {
			t.Fatalf("not idempotent: %q -> %q", got, redo)
		}
	})
}

// isSlugRune reports whether r is in the safe filename-slug character set
// (kept in sync with the sanitizer's switch).
func isSlugRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.'
}
