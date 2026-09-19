# bicep-ext-k8smanifest

[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

This is a custom Bicep local extension, written in Go. It turns Kubernetes
manifests authored in Bicep into standalone YAML files. No cluster is involved
— `bicep local-deploy` runs the extension locally, and each resource becomes a
validated YAML manifest under an output directory: one file per resource, or a
single combined multi-document file.

**Note:** this project was written with the assistance of LLMs running
entirely on local hardware — no code or project data was sent to any
external service.

## Why this exists

We want to deploy the cluster via GitOps, so the native manifest files need to
live in the repository as plain YAML. Microsoft's own
[Bicep Kubernetes extension](https://learn.microsoft.com/en-us/azure/azure-resource-manager/bicep/bicep-kubernetes-extension)
(preview) does not fit that: it deploys straight to the cluster during the ARM
deployment — it runs in the Azure management plane and takes a `kubeConfig`
(cluster admin credentials) — and it is not currently supported for private
clusters. This extension only produces the YAML; the deploy step belongs to
the GitOps tooling.

## How it works

In short: you write the manifest bodies in Bicep, the extension validates
them, adds `apiVersion` and `kind`, and writes the YAML.

1) The Bicep file declares `targetScope = 'local'` and
   `extension k8smanifest with { outputDir: ..., combine: ... }`. Each
   `resource x '<group>/<Kind>@<version>'` body is the manifest body
   (`metadata`, `spec`, `data`, …).
2) The extension resolves the kind to its `apiVersion`, validates
   `metadata.name` and the scope (namespaced vs cluster-scoped), and writes
   the YAML. `apiVersion` and `kind` are added by the extension — they are
   never taken from the body.
3) The output is deterministic. File names are stable
   (`<kind>-<namespace>-<name>.yaml` for namespaced kinds), keys are sorted,
   and with `combine: true` a single `combined.yaml` is written, sorted by a
   kubectl/helm-style deploy priority, so a one-pass `kubectl apply` works.
4) The resource bodies are fully typed in the IDE. Types are generated from
   the Kubernetes OpenAPI swagger into `src/gen/` by `src/cmd/gentypes`, which
   gives hover descriptions, typed inputs and required-flag enforcement
   (BCP035/BCP036). Around 50 kinds are catalogued — `core`, `apps/v1`,
   `batch/v1`, `networking.k8s.io/v1`, `gateway.networking.k8s.io/v1`,
   `rbac/v1`, `storage.k8s.io/v1`, `apiextensions.k8s.io/v1` and more. Kinds
   without a type (CRDs, preview APIs) fall back to a permissive type.
5) Documents the typed bodies cannot model go through the
   `k8smanifest/Raw@v1` verbatim passthrough: the body is
   `{ document: loadYamlContent('...') }` and the extension emits the
   document as-is (`apiVersion`/`kind` from the document) — e.g. a
   SOPS-encrypted manifest whose `sops` metadata block the typed `Secret`
   schema rejects. See [docs/architecture.md](docs/architecture.md).

Full details — type coverage, YAML correctness, repository layout, Flux
deployment and the open items — are in
[docs/architecture.md](docs/architecture.md).

## Requirements

- Go — the module lives in `src/`
- Bicep 0.47 or later

## Build & run

1) Build and publish the extension, from the repo root (`EXT_VERSION` is
   the single version input — it stamps the binary and the generated type
   index, and is the value the OCI publish uses as the tag):

```bash
EXT_VERSION=0.1.10 ./scripts/build.sh
```

This regenerates `src/gen/` from `swagger/`, builds
`bin/bicep-ext-k8smanifest` and publishes the local extension package to
`bin/bicep-ext-k8smanifest-pkg`.

2) Run the tests (unit + e2e):

```bash
go -C src test ./...
```

3) Point a `bicepconfig.json` at the extension. Register the local package
   (the path is **relative to the config file**, not the cwd) — the
   committed sample does exactly this:

```json
{
  "experimentalFeaturesEnabled": {
    "localDeploy": true
  },
  "extensions": {
    "k8smanifest": "../../bin/bicep-ext-k8smanifest-pkg"
  }
}
```

For shared or multi-machine use, publish the extension to an OCI registry
and reference it with a `br:` URI:

```json
{
  "experimentalFeaturesEnabled": {
    "localDeploy": true,
    "ociEnabled": true
  },
  "extensions": {
    "k8smanifest": "br:<registry>/bicep-ext-k8smanifest:0.1.10"
  }
}
```

- Bicep's OCI artifact support is a **preview** feature (hence the
  `ociEnabled` experimental flag) and is **registry-agnostic**: any OCI
  Distribution registry works — GHCR, ACR, Docker Hub, Harbor, …
- Publish with
  `bicep publish-extension --target br:<registry>/<repo>:<version>`
  (`scripts/release-build.sh` builds the multi-platform set; the CI
  `publish` job runs this and verifies the manifest).
- Consumers need registry credentials (`docker login` or the `DOCKER_*`
  environment variables) and, where the registry requires it,
  `BICEP_TRUSTED_REGISTRIES`.

4) Generate the examples' manifests:

```bash
# from samples/bicep/hello — the outputDir param resolves against the CLI
# working directory, so this must be run from that directory
bicep local-deploy dev.bicepparam

# and the same from samples/bicep/raw for the raw passthrough example
```

This writes `samples/manifests/hello/dev/combined.yaml` (a Deployment and a
Service) and `samples/manifests/raw/dev/combined.yaml` (two ConfigMaps
through the raw passthrough type), ready for:

```bash
kubectl apply -f samples/manifests/hello/dev/combined.yaml
kubectl apply -f samples/manifests/raw/dev/combined.yaml
```

One note on the committed generated manifests: after any code change that
doesn't alter behaviour, they must regenerate byte-identical. Regenerate them;
never edit them by hand.

## Examples

- `samples/bicep/hello/` — the typed path: one Deployment and one Service
  into the existing `default` namespace.
- `samples/bicep/raw/` — the raw passthrough: two ConfigMaps through
  `k8smanifest/Raw@v1` (one from a file, one inline), for documents the
  typed bodies cannot express.

## Open items

Known gaps are tracked in
[docs/architecture.md](docs/architecture.md#open-items).

## Documentation

| Document | Purpose |
|----------|---------|
| [docs/architecture.md](docs/architecture.md) | Technical reference: how the extension works, type coverage, YAML correctness, layout, build/run, Flux, publishing, open items |
| [CONTRIBUTING.md](CONTRIBUTING.md) | How to build, test and contribute (determinism contract, generated files, versioning) |

## Licence

[MIT License](LICENSE), Copyright (c) 2026 lumunro. Free to use, modify
and redistribute, including commercially. The software is provided "AS IS",
without warranty of any kind.

*Bicep* is a trademark of Microsoft. This is an independent, unofficial
project and is not affiliated with or endorsed by Microsoft.

### Third-party components

This repository vendors third-party input files for pinning and type
generation. Each keeps its own licence; the notes below are provenance
information for the pins (update them via the `scripts/update-*.sh` scripts):

* `swagger/swagger.json` — the Kubernetes OpenAPI swagger definition, pinned
  to the kubernetes/kubernetes release tag v1.36.4
  (`api/openapi-spec/swagger.json`; update via `scripts/update-swagger.sh`),
  from the Kubernetes project (https://github.com/kubernetes/kubernetes),
  Copyright 2014 The Kubernetes Authors. Licensed under the Apache License,
  Version 2.0.
* `swagger/crd/gateway-api.json` — the Gateway API CRD schemas for the
  catalogued gateway kinds (Gateway v1, HTTPRoute v1, ReferenceGrant v1beta1),
  pinned to the kubernetes-sigs/gateway-api release tag v1.6.2
  (`config/crd/standard`; update via `scripts/update-gateway-api.sh`), from
  the Gateway API project
  (https://github.com/kubernetes-sigs/gateway-api), Copyright 2020 The
  Kubernetes Authors. Licensed under the Apache License, Version 2.0.
* `swagger/crd/snapshotter.json` — the CSI snapshot CRD schemas for the
  catalogued snapshot kinds (VolumeSnapshot, VolumeSnapshotClass,
  VolumeSnapshotContent, all v1), pinned to the
  kubernetes-csi/external-snapshotter release tag v8.6.0 (`client/config/crd`;
  update via `scripts/update-snapshotter.sh`), from the external-snapshotter
  project (https://github.com/kubernetes-csi/external-snapshotter), licensed
  under the Apache License, Version 2.0.
* `src/bicep.azure.com/protos/` — the Bicep extension gRPC contract, generated
  from the Azure/bicep reference repository
  (https://github.com/Azure/bicep) via its `update_protobuf.sh`.
  Copyright (c) Microsoft Corporation. Licensed under the MIT License.
