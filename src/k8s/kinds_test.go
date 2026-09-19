// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

// Tests for the kind catalogue helpers shared by the extension runtime and
// the type generator (cmd/gentypes).
package k8s

import "testing"

// TestKindHelpers pins the Bicep type name, apiVersion and type reference
// formats for the group shapes (core, grouped, grouped with a beta version).
func TestKindHelpers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind       Kind
		bicepType  string
		apiVersion string
		ref        string
	}{
		{Kind{Kind: "Service", Version: "v1", Namespaced: true}, "core/Service", "v1", "core/Service@v1"},
		{Kind{Kind: "Namespace", Version: "v1"}, "core/Namespace", "v1", "core/Namespace@v1"},
		{Kind{Kind: "Deployment", Group: GrpApps, Version: "v1", Namespaced: true}, "apps/Deployment", "apps/v1", "apps/Deployment@v1"},
		{Kind{Kind: "Gateway", Group: GrpGateway, Version: "v1", Namespaced: true}, "gateway.networking.k8s.io/Gateway", "gateway.networking.k8s.io/v1", "gateway.networking.k8s.io/Gateway@v1"},
		{Kind{Kind: "ReferenceGrant", Group: GrpGateway, Version: "v1beta1", Namespaced: true}, "gateway.networking.k8s.io/ReferenceGrant", "gateway.networking.k8s.io/v1beta1", "gateway.networking.k8s.io/ReferenceGrant@v1beta1"},
	}

	for _, tc := range cases {
		t.Run(tc.bicepType, func(t *testing.T) {
			t.Parallel()

			if got := tc.kind.BicepTypeName(); got != tc.bicepType {
				t.Errorf("BicepTypeName = %q, want %q", got, tc.bicepType)
			}

			if got := tc.kind.APIVersion(); got != tc.apiVersion {
				t.Errorf("APIVersion = %q, want %q", got, tc.apiVersion)
			}

			if got := tc.kind.BicepTypeReference(); got != tc.ref {
				t.Errorf("BicepTypeReference = %q, want %q", got, tc.ref)
			}
		})
	}
}

// TestKindCatalogueInvariants: Bicep type names must be unique across the
// catalogue — LookupKind returns the first match, so a collision would
// silently mis-resolve one of the two entries (the catalogue deliberately
// carries two "Event" kinds in different groups).
func TestKindCatalogueInvariants(t *testing.T) {
	t.Parallel()

	seen := map[string]Kind{}

	for _, k := range Kinds {
		if k.Kind == "" || k.Version == "" {
			t.Errorf("incomplete catalogue entry: %+v", k)
		}

		if prev, dup := seen[k.BicepTypeName()]; dup {
			t.Errorf("duplicate Bicep type name %q: %+v and %+v", k.BicepTypeName(), prev, k)
		}

		seen[k.BicepTypeName()] = k
	}

	// Scope spot-checks for the kinds where a wrong entry would produce
	// manifests kubectl cannot apply (see AGENTS.md).
	wantNamespaced := map[string]bool{
		"core/Namespace":                           false,
		"core/PersistentVolume":                    false,
		"storage.k8s.io/VolumeAttachment":          false,
		"snapshot.storage.k8s.io/VolumeSnapshot":   true,
		"gateway.networking.k8s.io/Gateway":        true,
		"gateway.networking.k8s.io/ReferenceGrant": true,
	}

	for name, want := range wantNamespaced {
		k, ok := LookupKind(name)
		if !ok {
			t.Errorf("LookupKind(%q) = not found", name)

			continue
		}

		if k.Namespaced != want {
			t.Errorf("%s Namespaced = %v, want %v", name, k.Namespaced, want)
		}
	}
}

// TestLookupKind: known names resolve exactly (case-sensitive), unknown
// names (CRD groups, typos) report not-found so the caller falls back.
func TestLookupKind(t *testing.T) {
	t.Parallel()

	k, ok := LookupKind("apps/Deployment")
	if !ok || k.Kind != "Deployment" || k.Group != GrpApps || !k.Namespaced {
		t.Errorf("LookupKind(apps/Deployment) = %+v, %v", k, ok)
	}

	for _, unknown := range []string{"example.com/Gadget", "apps/deployment", "core/Widget"} {
		if _, ok := LookupKind(unknown); ok {
			t.Errorf("LookupKind(%q) matched, want no match", unknown)
		}
	}
}
