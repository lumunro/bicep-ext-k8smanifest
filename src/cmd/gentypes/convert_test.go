// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

// Unit tests for the swagger -> Bicep type conversion helpers. The committed
// gen/ files only prove the shapes the swagger happens to contain; these
// pin each shape handler (enums, constraints, composites, memoisation)
// directly.
package main

import (
	"testing"

	"github.com/Azure/bicep-types/src/bicep-types-go/types"
)

// Bicep type discriminators (repeated across the assertions below).
const (
	schemaTypeString = "string" // swagger "type" discriminators used in the schemas below
	schemaTypeObject = "object"
	schemaTypeArray  = "array"

	typAny     = "AnyType"
	typString  = "StringType"
	typBoolean = "BooleanType"
)

// mustBe is the comma-ok type assertion the lint config requires (errcheck
// with check-type-assertions) without tripping forcetypeassert at the call
// sites.
func mustBe[T types.Type](t *testing.T, x any, name string) T {
	t.Helper()

	v, ok := x.(T)
	if !ok {
		t.Fatalf("%s is %T, want %T", name, x, new(T))
	}

	return v
}

// typeAt resolves a factory reference to the type it points at.
func typeAt(t *testing.T, g *generator, ref types.ITypeReference) types.Type {
	t.Helper()

	tr, ok := ref.(types.TypeReference)
	if !ok {
		t.Fatalf("reference is %T, want types.TypeReference", ref)
	}

	typ, err := g.fac.GetTypeByIndex(tr.Ref)
	if err != nil {
		t.Fatalf("GetTypeByIndex(%d): %v", tr.Ref, err)
	}

	return typ
}

// TestConvertNilUnknownAndRef: nil schemas and unrecognised types fall back
// to any; a $ref resolves through the catalogue of definitions.
func TestConvertNilUnknownAndRef(t *testing.T) {
	t.Parallel()

	g := newGenerator(nil)

	if typ := typeAt(t, g, g.convert(nil, "x")); typ.Type() != typAny {
		t.Errorf("nil schema = %s, want AnyType", typ.Type())
	}

	// "number" is not one of the handled swagger shapes -> any (see AGENTS.md:
	// it needs a dedicated case before a definition using it can be typed).
	if typ := typeAt(t, g, g.convert(&schema{Type: "number"}, "x")); typ.Type() != typAny {
		t.Errorf("number type = %s, want AnyType", typ.Type())
	}

	// The "type" field may be a string array: the first entry wins.
	if typ := typeAt(t, g, g.convert(&schema{Type: []any{schemaTypeString}}, "x")); typ.Type() != typString {
		t.Errorf("string-array type = %s, want StringType", typ.Type())
	}

	// A $ref to a present non-object definition (IntOrString) resolves.
	g2 := newGenerator(map[string]*schema{"IoS": {Format: "int-or-string"}})

	u := mustBe[*types.UnionType](t, typeAt(t, g2, g2.convert(&schema{Ref: "#/definitions/IoS"}, "x")), "$ref to IntOrString definition")
	if len(u.Elements) != 2 {
		t.Errorf("IntOrString union has %d elements, want 2", len(u.Elements))
	}
}

// TestConvertIntOrString: both swagger markers produce the string|int union.
func TestConvertIntOrString(t *testing.T) {
	t.Parallel()

	g := newGenerator(nil)

	for _, s := range []*schema{{Format: "int-or-string"}, {XIntOrString: true}} {
		u := mustBe[*types.UnionType](t, typeAt(t, g, g.convert(s, "x")), "IntOrString")
		if len(u.Elements) != 2 {
			t.Errorf("IntOrString union has %d elements, want 2", len(u.Elements))
		}
	}
}

func TestConvertString(t *testing.T) {
	t.Parallel()

	g := newGenerator(nil)

	if typ := typeAt(t, g, g.convert(&schema{Type: schemaTypeString}, "name")); typ.Type() != typString {
		t.Errorf("plain string = %s, want StringType", typ.Type())
	}

	st := mustBe[*types.StringType](t, typeAt(t, g, g.convert(&schema{Type: schemaTypeString}, "spec.tls.privateKey")), "privateKey string")
	if !st.Sensitive {
		t.Error("privateKey string is not marked sensitive")
	}

	pat := mustBe[*types.StringType](t, typeAt(t, g, g.convert(&schema{Type: schemaTypeString, Pattern: "^[a-z]+$"}, "name")), "pattern string")
	if pat.Pattern != "^[a-z]+$" {
		t.Errorf("pattern = %q, want ^[a-z]+$", pat.Pattern)
	}

	// String enums become a union of string literals (IDE autocomplete).
	u := mustBe[*types.UnionType](t, typeAt(t, g, g.convert(&schema{Type: schemaTypeString, Enum: []any{"Running", "Succeeded"}}, "status")), "status enum")
	if len(u.Elements) != 2 {
		t.Fatalf("enum union has %d elements, want 2", len(u.Elements))
	}

	lit := mustBe[*types.StringLiteralType](t, typeAt(t, g, u.Elements[0]), "enum element 0")
	if lit.Value != "Running" {
		t.Errorf("first literal = %q, want Running", lit.Value)
	}
}

func TestConvertArray(t *testing.T) {
	t.Parallel()

	g := newGenerator(nil)

	arr := mustBe[*types.ArrayType](t, typeAt(t, g, g.convert(&schema{Type: schemaTypeArray, Items: &schema{Type: schemaTypeString}}, "x")), "array of string")
	if typ := typeAt(t, g, arr.ItemType); typ.Type() != typString {
		t.Errorf("array item = %s, want StringType", typ.Type())
	}

	bare := mustBe[*types.ArrayType](t, typeAt(t, g, g.convert(&schema{Type: schemaTypeArray}, "x")), "itemless array")
	if typ := typeAt(t, g, bare.ItemType); typ.Type() != typAny {
		t.Errorf("itemless array item = %s, want AnyType", typ.Type())
	}

	minLen, maxLen := int64(1), int64(3)
	bounded := mustBe[*types.ArrayType](t, typeAt(t, g, g.convert(&schema{Type: schemaTypeArray, Items: &schema{Type: schemaTypeString}, MinLength: &minLen, MaxLength: &maxLen}, "x")), "bounded array")

	if bounded.MinLength == nil || *bounded.MinLength != minLen || bounded.MaxLength == nil || *bounded.MaxLength != maxLen {
		t.Errorf("length constraints = %v..%v, want %d..%d", bounded.MinLength, bounded.MaxLength, minLen, maxLen)
	}
}

func TestConvertObject(t *testing.T) {
	t.Parallel()

	g := newGenerator(nil)

	obj := mustBe[*types.ObjectType](t, typeAt(t, g, g.convert(&schema{Type: schemaTypeObject, Properties: map[string]*schema{"a": {Type: schemaTypeString}}}, "Box")), "object Box")
	if obj.Name != "Box" || len(obj.Properties) != 1 {
		t.Fatalf("object = name %q with %d properties, want Box with 1", obj.Name, len(obj.Properties))
	}

	if typ := typeAt(t, g, obj.Properties["a"].Type); typ.Type() != typString {
		t.Errorf("property a = %s, want StringType", typ.Type())
	}

	// preserve-unknown-fields -> permissive (any) additional properties.
	raw := mustBe[*types.ObjectType](t, typeAt(t, g, g.convert(&schema{Type: schemaTypeObject, XPreserveUnknown: true}, "Raw")), "preserve-unknown object")
	if raw.AdditionalProperties == nil {
		t.Fatal("preserve-unknown object has no additionalProperties")
	}

	if typ := typeAt(t, g, raw.AdditionalProperties); typ.Type() != typAny {
		t.Errorf("preserve-unknown additionalProperties = %s, want AnyType", typ.Type())
	}

	// additionalProperties schema -> typed map values.
	mapped := mustBe[*types.ObjectType](t, typeAt(t, g, g.convert(&schema{Type: schemaTypeObject, AdditionalProperties: &schema{Type: schemaTypeString}}, "Map")), "map object")
	if typ := typeAt(t, g, mapped.AdditionalProperties); typ.Type() != typString {
		t.Errorf("map additionalProperties = %s, want StringType", typ.Type())
	}

	// A bare object (no properties, no additionalProperties) falls back to
	// any — the swagger requires nothing.
	if typ := typeAt(t, g, g.convert(&schema{Type: schemaTypeObject}, "Bare")); typ.Type() != typAny {
		t.Errorf("bare object = %s, want AnyType", typ.Type())
	}
}

// TestAnonObjectMemoized: the same nameHint must return the same reference,
// so the type file holds one entry for a shape that recurs.
func TestAnonObjectMemoized(t *testing.T) {
	t.Parallel()

	g := newGenerator(nil)

	props := map[string]*schema{"a": {Type: schemaTypeString}}
	first := g.convert(&schema{Type: schemaTypeObject, Properties: props}, "Box")

	second := g.convert(&schema{Type: schemaTypeObject, Properties: props}, "Box")

	f1, ok1 := first.(types.TypeReference)

	f2, ok2 := second.(types.TypeReference)
	if !ok1 || !ok2 || f1.Ref != f2.Ref {
		t.Errorf("memoisation broken: %v vs %v", first, second)
	}
}

func TestConvertComposite(t *testing.T) {
	t.Parallel()

	g := newGenerator(nil)

	anyOf := mustBe[*types.UnionType](t, typeAt(t, g, g.convert(&schema{AnyOf: []*schema{{Type: schemaTypeString}, {Type: "integer"}}}, "x")), "anyOf")
	if len(anyOf.Elements) != 2 {
		t.Errorf("anyOf union has %d elements, want 2", len(anyOf.Elements))
	}

	oneOf := mustBe[*types.UnionType](t, typeAt(t, g, g.convert(&schema{OneOf: []*schema{{Type: "boolean"}, {Type: schemaTypeString}}}, "x")), "oneOf")
	if len(oneOf.Elements) != 2 {
		t.Errorf("oneOf union has %d elements, want 2", len(oneOf.Elements))
	}
}

func TestConvertIntegerAndBoolean(t *testing.T) {
	t.Parallel()

	g := newGenerator(nil)

	lo, hi := float64(-5), float64(100)
	it := mustBe[*types.IntegerType](t, typeAt(t, g, g.convert(&schema{Type: "integer", Minimum: &lo, Maximum: &hi}, "x")), "bounded integer")

	if it.MinValue == nil || *it.MinValue != -5 || it.MaxValue == nil || *it.MaxValue != 100 {
		t.Errorf("integer constraints = %v..%v, want -5..100", it.MinValue, it.MaxValue)
	}

	if typ := typeAt(t, g, g.convert(&schema{Type: "boolean"}, "x")); typ.Type() != typBoolean {
		t.Errorf("boolean = %s, want BooleanType", typ.Type())
	}
}

func TestIsSensitive(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"spec.tls.privateKey":  true,
		"spec.clientKeyData":   true,
		"spec.PRIVATEKEY":      true,
		"metadata.name":        false,
		"spec.tls.certificate": false,
	}

	for name, want := range cases {
		if got := isSensitive(name); got != want {
			t.Errorf("isSensitive(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestInt64Ptr(t *testing.T) {
	t.Parallel()

	if int64Ptr(nil) != nil {
		t.Error("int64Ptr(nil) != nil")
	}

	f := 2.0
	got := int64Ptr(&f)

	if got == nil || *got != 2 {
		t.Errorf("int64Ptr(&2.0) = %v, want 2", got)
	}
}

// TestDefRefStrings drives scalarDefRef's string shapes through its public
// caller (defRef): plain, constrained and sensitive definitions.
func TestDefRefStrings(t *testing.T) {
	t.Parallel()

	minLen, maxLen := int64(3), int64(9)

	g := newGenerator(map[string]*schema{
		"plain":       {Type: schemaTypeString},
		"constrained": {Type: schemaTypeString, MinLength: &minLen, MaxLength: &maxLen, Pattern: "^a$"},
		"privatekey":  {Type: schemaTypeString},
	})

	st := mustBe[*types.StringType](t, typeAt(t, g, g.defRef("plain")), "plain string def")
	if st.MinLength != nil || st.MaxLength != nil || st.Pattern != "" || st.Sensitive {
		t.Errorf("plain string def = %+v, want no constraints, not sensitive", st)
	}

	cs := mustBe[*types.StringType](t, typeAt(t, g, g.defRef("constrained")), "constrained string def")
	if cs.MinLength == nil || *cs.MinLength != 3 || cs.MaxLength == nil || *cs.MaxLength != 9 || cs.Pattern != "^a$" {
		t.Errorf("constrained string def = %+v, want min 3 max 9 pattern ^a$", cs)
	}

	pk := mustBe[*types.StringType](t, typeAt(t, g, g.defRef("privatekey")), "sensitive string def")
	if !pk.Sensitive {
		t.Error("definition named privatekey must be sensitive")
	}
}

// TestDefRefOtherScalars: a bounded integer, a boolean, and the number -> any
// fallback (with its warning).
func TestDefRefOtherScalars(t *testing.T) {
	t.Parallel()

	lo, hi := float64(-5), float64(100)

	g := newGenerator(map[string]*schema{
		"bounded": {Type: "integer", Minimum: &lo, Maximum: &hi},
		"flag":    {Type: "boolean"},
		"ratio":   {Type: "number"},
	})

	it := mustBe[*types.IntegerType](t, typeAt(t, g, g.defRef("bounded")), "bounded integer def")
	if it.MinValue == nil || *it.MinValue != -5 || it.MaxValue == nil || *it.MaxValue != 100 {
		t.Errorf("bounded integer def = %v..%v, want -5..100", it.MinValue, it.MaxValue)
	}

	if typ := typeAt(t, g, g.defRef("flag")); typ.Type() != typBoolean {
		t.Errorf("boolean def = %s, want BooleanType", typ.Type())
	}

	// number has no float type in bicep-types-go: warn and fall back to any.
	if typ := typeAt(t, g, g.defRef("ratio")); typ.Type() != typAny {
		t.Errorf("number def = %s, want AnyType", typ.Type())
	}

	if len(g.warnings) != 1 {
		t.Errorf("warnings = %v, want exactly the number fallback", g.warnings)
	}
}

// TestDefRefSpecial: int-or-string definitions become a string/integer union,
// an absent definition falls back to any with a warning, and refs are
// memoised (a second call returns the identical reference).
func TestDefRefSpecial(t *testing.T) {
	t.Parallel()

	g := newGenerator(map[string]*schema{
		"quantity": {Format: "int-or-string"},
		"port":     {Type: schemaTypeString},
	})

	u := mustBe[*types.UnionType](t, typeAt(t, g, g.defRef("quantity")), "int-or-string def")
	if len(u.Elements) != 2 {
		t.Errorf("int-or-string def has %d union elements, want 2", len(u.Elements))
	}

	absent := typeAt(t, g, g.defRef("no.such.Definition"))
	if absent.Type() != typAny {
		t.Errorf("absent def = %s, want AnyType", absent.Type())
	}

	if len(g.warnings) != 1 {
		t.Errorf("warnings = %v, want the absent-definition warning", g.warnings)
	}

	first := g.defRef("port")

	second := g.defRef("port")
	if first != second {
		t.Errorf("defRef not memoised: %v vs %v", first, second)
	}
}

// TestRequiredFor: the swagger "required" array and the curated overlay both
// mark properties required; an overlay entry for a property that is absent
// from the definition warns and is skipped.
func TestRequiredFor(t *testing.T) {
	t.Parallel()

	g := newGenerator(nil)

	s := &schema{
		Required:   []string{"a"},
		Properties: map[string]*schema{"a": {Type: schemaTypeString}, "b": {Type: schemaTypeString}},
	}

	// PodSpec's overlay requires "containers", which is absent from s here.
	required := g.requiredFor("io.k8s.api.core.v1.PodSpec", s)
	if !required["a"] {
		t.Error("swagger-required property a must be required")
	}

	if required["b"] {
		t.Error("unrequired property b must not be required")
	}

	if required["containers"] {
		t.Error("overlay property containers must be skipped (absent from the definition)")
	}

	if len(g.warnings) != 1 {
		t.Errorf("warnings = %v, want the skipped-overlay warning", g.warnings)
	}

	// With the overlay property present it is marked required and no second
	// warning fires.
	s.Properties["containers"] = &schema{Type: schemaTypeArray}

	required = g.requiredFor("io.k8s.api.core.v1.PodSpec", s)
	if !required["containers"] {
		t.Error("overlay property containers must be required when present")
	}

	if len(g.warnings) != 1 {
		t.Errorf("warnings = %v, want no second warning for the present overlay property", g.warnings)
	}
}

// TestDefRefTopLevelArray: a top-level array definition is routed through the
// item-converting path (not an object type) and memoised.
func TestDefRefTopLevelArray(t *testing.T) {
	t.Parallel()

	g := newGenerator(map[string]*schema{
		"list": {Type: schemaTypeArray, Items: &schema{Type: schemaTypeString}},
	})

	arr := mustBe[*types.ArrayType](t, typeAt(t, g, g.defRef("list")), "top-level array def")
	if typeAt(t, g, arr.ItemType).Type() != typString {
		t.Errorf("array item = %s, want StringType", typeAt(t, g, arr.ItemType).Type())
	}

	if _, ok := g.defsUsed["list"]; !ok {
		t.Error("top-level array def was not memoised into defsUsed")
	}
}
