#!/usr/bin/env bash
# Download the CSI snapshot CRDs for a pinned kubernetes-csi/external-snapshotter
# release tag and synthesise swagger-style definitions for the catalogued
# snapshot kinds (src/k8s/kinds.go) into swagger/crd/snapshotter.json — a
# CRD-schemas input to cmd/gentypes (merged with the core swagger). The core
# swagger never carries snapshot CRD kinds, so without this file they fall
# back to the permissive body type.
#
# Usage:
#   SNAPSHOTTER_VERSION=v8.6.0 scripts/update-snapshotter.sh
#
# The kind/version pairs below must match the snapshot.storage.k8s.io
# entries in src/k8s/kinds.go — keep them in sync (the script fails when a
# version is absent from a fetched CRD). The output is deterministic per
# tag (sorted keys), so a re-run for the same tag is byte-identical.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${SNAPSHOTTER_VERSION:?set SNAPSHOTTER_VERSION (a kubernetes-csi/external-snapshotter release tag, e.g. v8.6.0 — never main)}"
if ! [[ "$SNAPSHOTTER_VERSION" =~ ^v[0-9]+\.[0-9]+(\.[0-9]+)?$ ]]; then
  echo "ERROR: SNAPSHOTTER_VERSION must be a release tag like v8.6.0 (got '$SNAPSHOTTER_VERSION')" >&2
  exit 1
fi
if ! command -v yq >/dev/null; then
  echo "ERROR: yq is required (YAML -> JSON); install mikefarah/yq" >&2
  exit 1
fi

base="https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/$SNAPSHOTTER_VERSION/client/config/crd"
target="$root/swagger/crd/snapshotter.json"
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

# CRD file -> (Kind, version) pairs, matching src/k8s/kinds.go.
files=("volumesnapshots:VolumeSnapshot:v1" "volumesnapshotclasses:VolumeSnapshotClass:v1" "volumesnapshotcontents:VolumeSnapshotContent:v1")

for entry in "${files[@]}"; do
  IFS=':' read -r file kind ver <<< "$entry"
  yaml="$tmpdir/$file.yaml"
  echo "Fetching $base/snapshot.storage.k8s.io_${file}.yaml ..."
  # -f: fail on HTTP error status so a 404 page can never be parsed
  curl -sfL "$base/snapshot.storage.k8s.io_${file}.yaml" --max-time 60 -o "$yaml"
  # These CRDs use the full-object convention for openAPIV3Schema (it
  # carries apiVersion/kind/metadata/spec/status — VolumeSnapshotClass has
  # no spec at all: driver/parameters sit at the top level), so take the
  # whole schema and let the assembly below strip the envelope.
  if ! yq -o=json ".spec.versions[] | select(.name == \"$ver\") | .schema.openAPIV3Schema" "$yaml" > "$tmpdir/$kind.full.json"; then
    echo "ERROR: yq failed on $file.yaml" >&2
    exit 1
  fi
  if [ ! -s "$tmpdir/$kind.full.json" ] || ! python3 -c "import json,sys; d=json.load(open('$tmpdir/$kind.full.json')); p=d.get('properties',{}); sys.exit(0 if isinstance(d,dict) and d.get('type')=='object' and 'metadata' in p and 'apiVersion' in p else 1)"; then
    echo "ERROR: version $ver not found (or not a full-object schema) in $file.yaml for tag $SNAPSHOTTER_VERSION" >&2
    exit 1
  fi
done

python3 - "$tmpdir" "$target" "$SNAPSHOTTER_VERSION" <<'EOF'
import json, os, sys

tmpdir, target, tag = sys.argv[1], sys.argv[2], sys.argv[3]

# (kind, version) pairs — mirror the snapshot entries in src/k8s/kinds.go.
kinds = [("VolumeSnapshot", "v1"), ("VolumeSnapshotClass", "v1"), ("VolumeSnapshotContent", "v1")]
meta_ref = {"$ref": "#/definitions/io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta"}

defs = {}
for kind, ver in kinds:
    full = json.load(open(os.path.join(tmpdir, f"{kind}.full.json")))
    # Body shape = the CRD object minus the envelope: apiVersion/kind are
    # added by the extension (never taken from the body) and status is
    # server-managed. metadata is unified with the core swagger's ObjectMeta
    # instead of the CRD's copy. Kinds without a spec field (e.g.
    # VolumeSnapshotClass: driver/parameters at the top level) work as-is.
    props = {k: v for k, v in full.get("properties", {}).items()
             if k not in ("apiVersion", "kind", "status")}
    props["metadata"] = meta_ref

    name = f"io.k8s.api.snapshot.storage.k8s.io.{ver}.{kind}"
    defn = {
        "description": f"CSI {kind} ({ver}), from the kubernetes-csi/external-snapshotter CRD schema ({tag}).",
        "type": "object",
        "properties": props,
    }
    required = [r for r in full.get("required", []) if r in props]
    if required:
        defn["required"] = required
    defs[name] = defn

out = {"definitions": defs}
blob = json.dumps(out, indent=2, sort_keys=True) + "\n"
if len(blob) < 10_000:
    sys.exit(f"refusing: output is only {len(blob)} bytes (missing schemas?)")

tmp = target + ".tmp"
with open(tmp, "w") as f:
    f.write(blob)
os.replace(tmp, target)  # atomic: same directory
print(f"swagger/crd/snapshotter.json: {len(defs)} definitions ({', '.join(f'{k} {v}' for k, v in kinds)})")
EOF

sha=$(sha256sum "$target" | cut -d' ' -f1)

echo
echo "Updated $target from kubernetes-csi/external-snapshotter@$SNAPSHOTTER_VERSION (client/config/crd)"
echo "sha256: $sha"
echo
echo "Next: EXT_VERSION=<next> bash scripts/build.sh, then review 'git diff swagger/ src/gen/'"
