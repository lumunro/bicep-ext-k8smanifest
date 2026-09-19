// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"
)

// TestDeterministicOutputStableAcrossWriterOrders: the factory writer emits
// the type entries in an order that changes every run; deterministicOutput
// must produce the exact same bytes no matter what writer order the input
// arrived in (with the position-based $refs adjusted per input).
func TestDeterministicOutputStableAcrossWriterOrders(t *testing.T) {
	t.Parallel()

	// Writer order A: Zeta, Alpha, Beta, anonymous.
	typesA := "[\n" +
		`  {"$type": "StringType", "name": "Zeta"},` + "\n" +
		`  {"$type": "ObjectType", "name": "Alpha", "properties": {"v": {"$ref": "#/3"}}},` + "\n" +
		`  {"$type": "AnyType", "name": "Beta"},` + "\n" +
		`  {"$type": "AnyType"}` + "\n" +
		"]\n"

	indexA := `{"resources":{"x/Y@v1":{"$ref": "types.json#/2"}}}`

	// Writer order B: Beta, anonymous, Alpha, Zeta — same entries, refs
	// re-pointed to their own positions.
	typesB := "[\n" +
		`  {"$type": "AnyType", "name": "Beta"},` + "\n" +
		`  {"$type": "AnyType"},` + "\n" +
		`  {"$type": "ObjectType", "name": "Alpha", "properties": {"v": {"$ref": "#/1"}}},` + "\n" +
		`  {"$type": "StringType", "name": "Zeta"}` + "\n" +
		"]\n"

	indexB := `{"resources":{"x/Y@v1":{"$ref": "types.json#/0"}}}`

	outTypesA, outIndexA, err := deterministicOutput(typesA, indexA)
	if err != nil {
		t.Fatalf("deterministicOutput(A): %v", err)
	}

	outTypesB, outIndexB, err := deterministicOutput(typesB, indexB)
	if err != nil {
		t.Fatalf("deterministicOutput(B): %v", err)
	}

	if outTypesA != outTypesB {
		t.Errorf("types output differs between writer orders:\nA:\n%s\nB:\n%s", outTypesA, outTypesB)
	}

	if outIndexA != outIndexB {
		t.Errorf("index output differs between writer orders:\nA: %s\nB: %s", outIndexA, outIndexB)
	}

	// Sorted order: anonymous (name "") first, then Alpha, Beta, Zeta;
	// Alpha's ref to the anonymous entry is re-pointed to its new position.
	wantTypes := "[\n" +
		`  {"$type": "AnyType"},` + "\n" +
		`  {"$type": "ObjectType", "name": "Alpha", "properties": {"v": {"$ref": "#/0"}}},` + "\n" +
		`  {"$type": "AnyType", "name": "Beta"},` + "\n" +
		`  {"$type": "StringType", "name": "Zeta"}` + "\n" +
		"]"

	if outTypesA != wantTypes {
		t.Errorf("unexpected types output:\ngot:\n%s\nwant:\n%s", outTypesA, wantTypes)
	}

	if wantIndex := `{"resources":{"x/Y@v1":{"$ref": "types.json#/2"}}}`; outIndexA != wantIndex {
		t.Errorf("unexpected index output: got %s, want %s", outIndexA, wantIndex)
	}

	// Idempotent: determinizing the output again changes nothing.
	againTypes, againIndex, err := deterministicOutput(outTypesA, outIndexA)
	if err != nil {
		t.Fatalf("deterministicOutput(again): %v", err)
	}

	if againTypes != outTypesA || againIndex != outIndexA {
		t.Error("deterministicOutput is not idempotent")
	}
}

// TestDeterministicOutputMergesContentIdenticalEntries: the factory does
// not intern anonymous types, so content-identical entries (twins) appear
// multiple times; they must collapse to a single output entry with every
// ref to any member re-pointed to the survivor.
func TestDeterministicOutputMergesContentIdenticalEntries(t *testing.T) {
	t.Parallel()

	// E0 and E2 are identical StringTypes; E1 references the E2 twin.
	types := "[\n" +
		`  {"$type": "StringType"},` + "\n" +
		`  {"$type": "ArrayType", "itemType": {"$ref": "#/2"}},` + "\n" +
		`  {"$type": "StringType"}` + "\n" +
		"]"

	out, _, err := deterministicOutput(types, "")
	if err != nil {
		t.Fatalf("deterministicOutput: %v", err)
	}

	want := "[\n" +
		`  {"$type": "ArrayType", "itemType": {"$ref": "#/1"}},` + "\n" +
		`  {"$type": "StringType"}` + "\n" +
		"]"

	if out != want {
		t.Errorf("unexpected output:\ngot:\n%s\nwant:\n%s", out, want)
	}
}

// TestDeterministicOutputRejectsBadRefs: a position reference outside the
// entry array is left untouched rather than silently re-pointed.
func TestDeterministicOutputRejectsBadRefs(t *testing.T) {
	t.Parallel()

	// A position reference outside the entry array must be left untouched.
	const types = "[\n  {\"$type\": \"StringType\", \"name\": \"A\", \"d\": {\"$ref\": \"#/99\"}}\n]\n"

	out, _, err := deterministicOutput(types, "")
	if err != nil {
		t.Fatalf("deterministicOutput: %v", err)
	}

	if !strings.Contains(out, `"#/99"`) {
		t.Errorf("out-of-range ref was re-pointed: %s", out)
	}
}

// TestRefPosition: the position is the digits between `#/` and the closing
// quote; a match with no marker or a non-numeric body is an error.
func TestRefPosition(t *testing.T) {
	t.Parallel()

	n, err := refPosition([]byte(`"$ref": "types.json#/5"`))
	if err != nil || n != 5 {
		t.Errorf("refPosition(valid) = %d, %v; want 5, nil", n, err)
	}

	if _, err := refPosition([]byte(`"no marker"`)); err == nil {
		t.Error("refPosition(no marker) must error")
	}
}

// TestParseTypeEntries: valid input splits into raw + parsed entries; a
// non-object body is a top-level error and a non-object entry is a
// per-entry error.
func TestParseTypeEntries(t *testing.T) {
	t.Parallel()

	raws, parsed, err := parseTypeEntries(`[{"$type":"A"},{"$type":"B"}]`)
	if err != nil || len(raws) != 2 || len(parsed) != 2 {
		t.Fatalf("parseTypeEntries(valid) = %d raw, %d parsed, %v; want 2, 2, nil", len(raws), len(parsed), err)
	}

	if _, _, err := parseTypeEntries(`not json`); err == nil {
		t.Error("parseTypeEntries(non-JSON) must error")
	}

	if _, _, err := parseTypeEntries(`[42]`); err == nil {
		t.Error("parseTypeEntries(non-object entry) must error")
	}
}

// TestRemapCrossRefs: in-range cross-file refs are re-pointed to their new
// positions; out-of-range refs are left untouched.
func TestRemapCrossRefs(t *testing.T) {
	t.Parallel()

	in := `{"a": "$ref": "types.json#/0", "b": "$ref": "types.json#/99"}`
	out := remapCrossRefs(in, []int{10})

	if !strings.Contains(out, `"$ref": "types.json#/10"`) {
		t.Errorf("in-range ref not re-pointed: %s", out)
	}

	if !strings.Contains(out, `"$ref": "types.json#/99"`) {
		t.Errorf("out-of-range ref was re-pointed: %s", out)
	}
}
