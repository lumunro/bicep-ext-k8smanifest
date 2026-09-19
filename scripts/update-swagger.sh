#!/usr/bin/env bash
# Download the Kubernetes OpenAPI swagger (Swagger 2.0) for a pinned
# kubernetes/kubernetes release tag and atomically replace
# swagger/swagger.json.
#
# Usage:
#   K8S_VERSION=v1.33.4 scripts/update-swagger.sh
#
# K8S_VERSION must be a release tag (vX.Y[.Z]) — never master/main: the
# committed swagger is a pinned input and the generated src/gen/ files must
# stay byte-stable. After running this, run `EXT_VERSION=<next>
# scripts/build.sh`, review the `git diff` of swagger/ + src/gen/ (a newer
# swagger changes the generated types), and commit them together, carrying
# the provenance line printed below in the commit message.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${K8S_VERSION:?set K8S_VERSION (a kubernetes/kubernetes release tag, e.g. v1.33.4 — never master)}"
if ! [[ "$K8S_VERSION" =~ ^v[0-9]+\.[0-9]+(\.[0-9]+)?$ ]]; then
  echo "ERROR: K8S_VERSION must be a release tag like v1.33.4 (got '$K8S_VERSION')" >&2
  exit 1
fi

url="https://raw.githubusercontent.com/kubernetes/kubernetes/$K8S_VERSION/api/openapi-spec/swagger.json"
target="$root/swagger/swagger.json"
tmp=$(mktemp "$root/swagger/.swagger.json.XXXXXX")
trap 'rm -f "$tmp"' EXIT

echo "Fetching $url ..."
# -f: fail on HTTP error status so an HTML 404 page can never land in $target
curl -sfL "$url" --max-time 120 -o "$tmp"

# Validate before touching the committed file: the document must be a sane
# Swagger 2.0 spec with the expected Kubernetes definitions.
python3 - "$tmp" "$target" <<'EOF'
import json, os, sys

tmp, target = sys.argv[1], sys.argv[2]

size = os.path.getsize(tmp)
if size < 1_000_000:
    sys.exit(f"refusing: download is only {size} bytes (error page? truncated?)")

try:
    d = json.load(open(tmp))
except ValueError as e:
    sys.exit(f"refusing: not valid JSON: {e}")

if d.get("swagger") != "2.0":
    sys.exit(f"refusing: expected a Swagger 2.0 document, got swagger={d.get('swagger')!r}")
if d.get("info", {}).get("title") != "Kubernetes":
    sys.exit(f"refusing: unexpected info.title {d.get('info', {}).get('title')!r}")
defs = d.get("definitions") or {}
if len(defs) < 400:
    sys.exit(f"refusing: only {len(defs)} definitions (expected several hundred)")

old_count = 0
if os.path.exists(target):
    old_count = len(json.load(open(target)).get("definitions", {}))
print(f"swagger definitions: {len(defs)} (was {old_count})")
EOF

# Atomic replace: same-directory rename (the temp file lives in swagger/).
chmod 644 "$tmp"
mv -f "$tmp" "$target"
trap - EXIT
sha=$(sha256sum "$target" | cut -d' ' -f1)

echo
echo "Updated $target from kubernetes/kubernetes@$K8S_VERSION (api/openapi-spec/swagger.json)"
echo "sha256: $sha"
echo
echo "Next: EXT_VERSION=<next> bash scripts/build.sh, then review 'git diff swagger/ src/gen/'"
