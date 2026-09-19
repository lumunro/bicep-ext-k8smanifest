#!/usr/bin/env bash
# Dump the CRD schemas the examples depend on from the pinned lab cluster
# into swagger/crd/ — the second input to cmd/gentypes (merged with the core
# swagger). Unlike scripts/update-gateway-api.sh and update-snapshotter.sh
# (upstream download, pinned tag), these two CRDs come from the CLUSTER: the
# upstream download paths are unreachable from the build network, and the lab
# cluster runs the exact dataplane the examples' policies are validated
# against (scripts/validate-manifests.sh in the examples repo dumps these
# same CRDs live for kubeconform). The committed output is the source of
# truth; re-run after a cluster Cilium upgrade or a widget-CRD schema change
# (keep the examples repo's global-services/widget CRD in sync — it is the
# typed end-to-end CRD demo).
#
# Usage:
#   CILIUM_VERSION=v1.20.1 scripts/update-cluster-crds.sh
#
# Requires a kubeconfig pointed at the lab cluster (docs/test-cluster.md in
# the examples repo). Fails when the cluster's Cilium agent image does not
# carry CILIUM_VERSION — the types must match the dataplane that enforces
# the policies. The (crd, version, definition-name) pairs must match the
# cilium.io / example.com entries in src/k8s/kinds.go — keep them in sync.
set -euo pipefail

if [ -z "${CILIUM_VERSION:-}" ]; then
  echo "ERROR: set CILIUM_VERSION (the Cilium version the lab cluster runs, e.g. v1.20.1)" >&2
  exit 1
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

agent_image="$(kubectl -n kube-system get ds cilium -o jsonpath='{.spec.template.spec.containers[0].image}')"
if [[ "$agent_image" != *"$CILIUM_VERSION"* ]]; then
  echo "ERROR: cluster Cilium agent image '$agent_image' does not carry CILIUM_VERSION=$CILIUM_VERSION" >&2
  exit 1
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

# CRD names on the cluster.
crds=("ciliumnetworkpolicies.cilium.io" "widgets.example.com")
for crd in "${crds[@]}"; do
  kubectl get crd "$crd" -o json > "$tmpdir/$crd.json"
  echo "Fetched CRD $crd"
done

python3 - "$tmpdir" "$root" "$CILIUM_VERSION" <<'EOF'
import json, os, sys

tmpdir, root, cilium_version = sys.argv[1], sys.argv[2], sys.argv[3]

# (crd file, version, definition name, target file) — mirror the cilium.io /
# example.com entries in src/k8s/kinds.go. The definition name follows the
# swagger convention io.k8s.api.<group>.<version>.<Kind> (cmd/gentypes
# derives it from the catalogue via swaggerPrefix).
entries = [
    ("ciliumnetworkpolicies.cilium.io", "v2",
     "io.k8s.api.cilium.io.v2.CiliumNetworkPolicy",
     os.path.join(root, "swagger/crd/cilium-ciliumnetworkpolicy.json")),
    ("widgets.example.com", "v1",
     "io.k8s.api.example.com.v1.Widget",
     os.path.join(root, "swagger/crd/widget.json")),
]

meta_ref = {"$ref": "#/definitions/io.k8s.apimachinery.pkg.apis.meta.v1.ObjectMeta"}

for crd_file, version, def_name, target in entries:
    crd = json.load(open(os.path.join(tmpdir, crd_file + ".json")))
    vers = [v for v in crd["spec"]["versions"] if v["name"] == version]
    if not vers:
        sys.exit(f"ERROR: version {version} not served by CRD {crd_file}")
    full = vers[0]["schema"]["openAPIV3Schema"]
    props = full.get("properties", {})
    if not isinstance(full, dict) or full.get("type") != "object":
        sys.exit(f"ERROR: {crd_file} {version} schema is not an object")

    if "apiVersion" in props and "kind" in props:
        # Full-object convention (Cilium): the schema carries the whole
        # manifest; strip the envelope (apiVersion/kind are added by the
        # extension, status is server-managed) and unify metadata with the
        # core swagger's ObjectMeta.
        if "metadata" not in props:
            sys.exit(f"ERROR: {crd_file} {version} full-object schema has no metadata property")
        body_props = {k: v for k, v in props.items()
                      if k not in ("apiVersion", "kind", "status")}
        required = [r for r in full.get("required", []) if r in body_props]
    else:
        # Spec-only convention (widget): the CRD schema validates .spec, so
        # the body is metadata + the schema's top-level properties as-is.
        body_props = dict(props)
        required = [r for r in full.get("required", []) if r in body_props]
    body_props["metadata"] = meta_ref
    if "metadata" in required:
        required.remove("metadata")

    defn = {
        "description": (f"{def_name.rsplit('.', 1)[1]} "
                        f"({version}), from the {crd_file} CRD schema of the "
                        f"pinned lab cluster (Cilium {cilium_version}); "
                        f"refreshed by scripts/update-cluster-crds.sh."),
        "type": "object",
        "properties": body_props,
    }
    if required:
        defn["required"] = required

    out = {"definitions": {def_name: defn}}
    blob = json.dumps(out, indent=2, sort_keys=True) + "\n"
    if len(blob) < 400:
        sys.exit(f"refusing: {target} is only {len(blob)} bytes (missing schema?)")

    tmp = target + ".tmp"
    with open(tmp, "w") as f:
        f.write(blob)
    os.replace(tmp, target)  # atomic: same directory
    print(f"{os.path.relpath(target, root)}: {def_name} ({len(blob)} bytes)")
EOF

echo
echo "Next: EXT_VERSION=<next> bash scripts/build.sh, then review 'git diff swagger/ src/gen/'"
