#!/bin/bash
# Build the release matrix and assemble the release assets (the companion to
# scripts/build.sh, which builds one platform at a time):
#
#   bin/release/bicep-ext-k8smanifest-<os>-<arch>[.exe]  one binary per platform
#   bin/release/bicep-ext-k8smanifest-pkg-v<ver>.tgz     the multi-platform
#                                                        local extension package
#                                                        (bicep names the
#                                                        members linux-x64.bin,
#                                                        osx-arm64.bin, ...)
#   bin/release/SHA256SUMS                                checksums for the above
#
# The CI publish job uploads these as GitHub Release assets alongside the
# OCI registry publish (the registry stays the primary channel; the assets
# are the offline/portable fallback).
#
#   EXT_VERSION (required, x.y.z)  the single version input (see build.sh).
#   PLATFORMS   (optional, space-separated GOOS/GOARCH list; default: the
#               five standard targets).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${EXT_VERSION:?set EXT_VERSION (x.y.z)}"

if ! [[ "$EXT_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "ERROR: EXT_VERSION must be x.y.z (got '$EXT_VERSION')" >&2
  exit 1
fi

# windows/arm64 is skipped: Bicep itself is x64-only on Windows today, and
# the added asset weighs ~11 MB. Add it here when that changes.
PLATFORMS="${PLATFORMS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64}"

outdir="$root/bin/release"

rm -rf "$outdir"
mkdir -p "$outdir"

# The bicep package flag prefix differs from the Go GOOS name (darwin ->
# osx, windows -> win) and amd64 is x64. Input: "<os>-<arch>".
flag_for() {
  local os="${1%-*}" arch="${1#*-}" pfx a

  case "$os" in
  linux) pfx="linux" ;;
  darwin) pfx="osx" ;;
  windows) pfx="win" ;;
  *)
    echo "ERROR: unsupported GOOS '$os'" >&2
    return 1
    ;;
  esac

  a="$arch"
  if [ "$arch" = "amd64" ]; then
    a="x64"
  fi

  echo "--bin-${pfx}-${a}"
}

bins=()

for p in $PLATFORMS; do
  os="${p%/*}"
  arch="${p#*/}"

  out="$outdir/bicep-ext-k8smanifest-$os-$arch"
  if [ "$os" = "windows" ]; then
    out="$out.exe"
  fi

  echo "==> building $os/$arch"
  GOOS="$os" GOARCH="$arch" OUT="$out" SKIP_LOCAL_PACKAGE=1 EXT_VERSION="$EXT_VERSION" \
    bash "$root/scripts/build.sh"

  bins+=("$out")
done

# One multi-platform local package: Bicep picks the member matching the
# author's OS at local-deploy time.
pkg="$outdir/bicep-ext-k8smanifest-pkg-v$EXT_VERSION.tgz"
args=()

for out in "${bins[@]}"; do
  base="$(basename "$out" .exe)"
  plat="${base#bicep-ext-k8smanifest-}"

  flag="$(flag_for "$plat")"
  args+=("$flag" "$out")
done

# The flag list (one "<flag> <path>" pair per line) doubles as the input for
# the CI OCI publish step, which must ship the same multi-platform set to
# the registry (a linux-only blob would strand non-Linux br: consumers).
for ((i = 0; i < ${#args[@]}; i += 2)); do
  printf '%s %s\n' "${args[i]}" "${args[i+1]}"
done >"$outdir/bicep-flags.txt"

bicep_cmd=$(command -v bicep || echo "$HOME/.azure/bin/bicep")

"$bicep_cmd" publish-extension "${args[@]}" --target "$pkg" --force

(cd "$outdir" && sha256sum -- bicep-ext-k8smanifest-* >SHA256SUMS)

echo "==> release assets (v$EXT_VERSION) under $outdir:"
ls -lh "$outdir"

echo "==> multi-platform package contents:"
tar -tzf "$pkg"
