// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

// Package k8s holds the catalogue of Kubernetes resource kinds exposed by
// the bicep-ext-k8smanifest extension. It is shared by the extension runtime
// (package main) and the type generator (cmd/gentypes).
package k8s

import (
	"fmt"
	"strings"
)

// API group name constants (repeated across the catalogue below).
const (
	GrpApps     = "apps"
	GrpCilium   = "cilium.io"
	GrpExample  = "example.com"
	GrpGateway  = "gateway.networking.k8s.io"
	GrpRBAC     = "rbac.authorization.k8s.io"
	GrpSnapshot = "snapshot.storage.k8s.io"
	GrpStorage  = "storage.k8s.io"
)

// RawBicepType is the reserved Bicep type name for verbatim manifest
// passthrough. It is deliberately NOT in the Kinds catalogue (catalogued
// kinds get a typed body from the swagger; a raw body is the strict
// wrapper { document: <any> }, not a manifest body). It IS indexed in the
// type files — hand-authored by gentypes (handAuthoredTypes) alongside the
// fallback and configuration types — so the compiler type-checks the
// wrapper at compile time (a stray property beside document is BCP037) and
// no BCP081 is reported.
//
// Body shape: exactly { document: <manifest> }. Bicep resource bodies must
// be object literals (or if/for forms of them), so a manifest loaded with
// loadYamlContent cannot BE the body — it enters through the single
// document property. The wrapped document is the complete manifest: it
// carries its own apiVersion/kind, and everything else (the sops metadata
// block, ...) is emitted verbatim. See rawDocument in the main package.
const RawBicepType = "k8smanifest/Raw"

// IsRaw reports whether a Bicep resource type name is the raw passthrough
// type.
func IsRaw(bicepType string) bool {
	if i := strings.IndexByte(bicepType, '@'); i >= 0 {
		bicepType = bicepType[:i]
	}

	return bicepType == RawBicepType
}

// Kind is one Kubernetes resource kind exposed by the extension.
type Kind struct {
	// kind is the Kubernetes kind name (e.g. "Deployment").
	Kind string
	// Group is the full API group name; "" is the core group.
	Group string
	// Version is the API version (v1, v2, ...).
	Version string
	// Namespaced indicates the kind is namespaced; the runtime validates
	// metadata.namespace against it (see computeManifest).
	Namespaced bool
}

// Kinds is the catalogue of stable (non-preview) kinds from the Kubernetes
// API reference (kubernetes.io/docs/reference/...). The Bicep type name uses
// the API group as its prefix; the core group is conventionally named "core".
var Kinds = []Kind{
	// core (v1)
	{Kind: "Binding", Group: "", Version: "v1", Namespaced: true},
	{Kind: "ConfigMap", Group: "", Version: "v1", Namespaced: true},
	{Kind: "Endpoints", Group: "", Version: "v1", Namespaced: true},
	{Kind: "Event", Group: "", Version: "v1", Namespaced: true},
	{Kind: "LimitRange", Group: "", Version: "v1", Namespaced: true},
	{Kind: "Namespace", Group: "", Version: "v1", Namespaced: false},
	{Kind: "Node", Group: "", Version: "v1", Namespaced: false},
	{Kind: "PersistentVolume", Group: "", Version: "v1", Namespaced: false},
	{Kind: "PersistentVolumeClaim", Group: "", Version: "v1", Namespaced: true},
	{Kind: "Pod", Group: "", Version: "v1", Namespaced: true},
	{Kind: "PodTemplate", Group: "", Version: "v1", Namespaced: true},
	{Kind: "ReplicationController", Group: "", Version: "v1", Namespaced: true},
	{Kind: "ResourceQuota", Group: "", Version: "v1", Namespaced: true},
	{Kind: "Secret", Group: "", Version: "v1", Namespaced: true},
	{Kind: "Service", Group: "", Version: "v1", Namespaced: true},
	{Kind: "ServiceAccount", Group: "", Version: "v1", Namespaced: true},
	// apps (apps/v1)
	{Kind: "ControllerRevision", Group: GrpApps, Version: "v1", Namespaced: true},
	{Kind: "DaemonSet", Group: GrpApps, Version: "v1", Namespaced: true},
	{Kind: "Deployment", Group: GrpApps, Version: "v1", Namespaced: true},
	{Kind: "ReplicaSet", Group: GrpApps, Version: "v1", Namespaced: true},
	{Kind: "StatefulSet", Group: GrpApps, Version: "v1", Namespaced: true},
	// batch (batch/v1)
	{Kind: "CronJob", Group: "batch", Version: "v1", Namespaced: true},
	{Kind: "Job", Group: "batch", Version: "v1", Namespaced: true},
	// networking.k8s.io (v1)
	{Kind: "Ingress", Group: "networking.k8s.io", Version: "v1", Namespaced: true},
	{Kind: "IngressClass", Group: "networking.k8s.io", Version: "v1", Namespaced: false},
	{Kind: "NetworkPolicy", Group: "networking.k8s.io", Version: "v1", Namespaced: true},
	// cilium.io (v2) — CiliumNetworkPolicy: the zero-trust egress policy kind
	// the examples' egress module emits (the toFQDNs/toServices/toEntities
	// rules plus the DNS-intercept entry). Installed by the lab cluster's
	// Cilium daemon, NOT part of the core swagger; the body type comes from
	// the committed CRD schema (swagger/crd/cilium-ciliumnetworkpolicy.json,
	// refreshed by scripts/update-cluster-crds.sh from the pinned lab
	// cluster), merged in by cmd/gentypes.
	{Kind: "CiliumNetworkPolicy", Group: GrpCilium, Version: "v2", Namespaced: true},
	// gateway.networking.k8s.io (v1) — Gateway API (installed as CRDs; the
	// core swagger has no gateway definitions). The body types come from the
	// committed Gateway API CRD schemas (swagger/gateway-api.json, refreshed
	// by scripts/update-gateway-api.sh), which cmd/gentypes merges alongside
	// the core swagger. ReferenceGrant is v1beta1: the Gateway API v1 group
	// carries Gateway/HTTPRoute only.
	{Kind: "Gateway", Group: GrpGateway, Version: "v1", Namespaced: true},
	{Kind: "HTTPRoute", Group: GrpGateway, Version: "v1", Namespaced: true},
	{Kind: "ReferenceGrant", Group: GrpGateway, Version: "v1beta1", Namespaced: true},
	// rbac.authorization.k8s.io (v1)
	{Kind: "ClusterRole", Group: GrpRBAC, Version: "v1", Namespaced: false},
	{Kind: "ClusterRoleBinding", Group: GrpRBAC, Version: "v1", Namespaced: false},
	{Kind: "Role", Group: GrpRBAC, Version: "v1", Namespaced: true},
	{Kind: "RoleBinding", Group: GrpRBAC, Version: "v1", Namespaced: true},
	// autoscaling (v2)
	{Kind: "HorizontalPodAutoscaler", Group: "autoscaling", Version: "v2", Namespaced: true},
	// policy (v1)
	{Kind: "PodDisruptionBudget", Group: "policy", Version: "v1", Namespaced: true},
	// storage.k8s.io (v1)
	{Kind: "CSIDriver", Group: GrpStorage, Version: "v1", Namespaced: false},
	{Kind: "CSINode", Group: GrpStorage, Version: "v1", Namespaced: false},
	{Kind: "StorageClass", Group: GrpStorage, Version: "v1", Namespaced: false},
	{Kind: "VolumeAttachment", Group: GrpStorage, Version: "v1", Namespaced: false},
	// snapshot.storage.k8s.io (v1) — the CSI snapshot CRDs; NOT part of the
	// storage.k8s.io group (the extension emits the group in apiVersion,
	// so a wrong group here produces manifests kubectl cannot apply). Like
	// the gateway kinds, the core swagger never carries these CRDs; the body
	// types come from the committed external-snapshotter CRD schemas in
	// swagger/crd/ (scripts/update-snapshotter.sh), merged in by cmd/gentypes.
	{Kind: "VolumeSnapshot", Group: GrpSnapshot, Version: "v1", Namespaced: true},
	{Kind: "VolumeSnapshotClass", Group: GrpSnapshot, Version: "v1", Namespaced: false},
	{Kind: "VolumeSnapshotContent", Group: GrpSnapshot, Version: "v1", Namespaced: false},
	// coordination.k8s.io (v1)
	{Kind: "Lease", Group: "coordination.k8s.io", Version: "v1", Namespaced: true},
	// scheduling.k8s.io (v1)
	{Kind: "PriorityClass", Group: "scheduling.k8s.io", Version: "v1", Namespaced: false},
	// node.k8s.io (v1)
	{Kind: "RuntimeClass", Group: "node.k8s.io", Version: "v1", Namespaced: false},
	// certificates.k8s.io (v1)
	{Kind: "CertificateSigningRequest", Group: "certificates.k8s.io", Version: "v1", Namespaced: false},
	// apiextensions.k8s.io (v1)
	{Kind: "CustomResourceDefinition", Group: "apiextensions.k8s.io", Version: "v1", Namespaced: false},
	// apiregistration.k8s.io (v1)
	{Kind: "APIService", Group: "apiregistration.k8s.io", Version: "v1", Namespaced: false},
	// events.k8s.io (v1)
	{Kind: "Event", Group: "events.k8s.io", Version: "v1", Namespaced: true},
	// flowcontrol.apiserver.k8s.io (v1)
	{Kind: "FlowSchema", Group: "flowcontrol.apiserver.k8s.io", Version: "v1", Namespaced: false},
	{Kind: "PriorityLevelConfiguration", Group: "flowcontrol.apiserver.k8s.io", Version: "v1", Namespaced: false},
	// example.com (v1) — Widget: the examples repo's fixture CRD (its
	// global-services/widget stack) — the typed end-to-end CRD demo
	// (CRD definition -> CR instance). Same source story as the Cilium
	// entry above (scripts/update-cluster-crds.sh); keep the schema in
	// sync with the examples repo's committed CRD.
	{Kind: "Widget", Group: GrpExample, Version: "v1", Namespaced: true},
}

// BicepTypeName returns the Bicep resource type name for a kind
// ("core/Service", "apps/Deployment", ...).
func (k Kind) BicepTypeName() string {
	group := k.Group
	if group == "" {
		group = "core"
	}

	return fmt.Sprintf("%s/%s", group, k.Kind)
}

// BicepTypeReference is the full "<group>/<Kind>@<version>" Bicep resource
// type name. The version suffix is required: the type index is keyed by it
// and Bicep resolves the deployed resource's TypeReference from the
// declared type name, so without the version the ApiVersion is lost and
// Bicep reports BCP081 ("does not have types available").
func (k Kind) BicepTypeReference() string {
	return fmt.Sprintf("%s@%s", k.BicepTypeName(), k.Version)
}

// APIVersion returns the Kubernetes apiVersion string for a kind.
func (k Kind) APIVersion() string {
	if k.Group == "" {
		return "v1"
	}

	return fmt.Sprintf("%s/%s", k.Group, k.Version)
}

// LookupKind resolves a Bicep resource type name to its catalogue entry.
// Unknown names (e.g. CRD groups) are handled by the caller.
func LookupKind(bicepType string) (Kind, bool) {
	for _, k := range Kinds {
		if k.BicepTypeName() == bicepType {
			return k, true
		}
	}

	return Kind{}, false
}
