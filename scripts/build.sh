#!/bin/bash
# Build the extension binary and (by default) publish it as a local Bicep
# extension package that the samples' bicepconfig.json points at.
#
# EXT_VERSION (required, e.g. 0.1.2) is the single version input: it is
# stamped into the binary (-ldflags -X, reported by `--version` and the
# startup log), into gen/index.json settings.version (via gentypes -version),
# and is the same value scripts/oci-publish.sh publishes as the OCI tag.
#
# Optional environment knobs (the defaults keep the historical behaviour):
#   GOOS / GOARCH       target platform (default: host). A cross build skips
#                       the --version stamp check — a foreign binary cannot
#                       run here, and the host build in the same flow covers
#                       the -X path.
#   OUT                 output path (default: bin/bicep-ext-k8smanifest,
#                       .exe on Windows).
#   SKIP_LOCAL_PACKAGE  =1 skips the bicep publish-extension step (the
#                       release flow assembles one multi-platform package
#                       itself — see scripts/release-build.sh). A cross build
#                       also skips it automatically: the local package is
#                       consumed on the author's own machine.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${EXT_VERSION:?set EXT_VERSION (e.g. EXT_VERSION=0.1.2) — stamped into the binary and gen/index.json}"

# env -u: go env honours GOOS/GOARCH overrides, so the real host platform
# must be read with them stripped.
host_os="$(env -u GOOS go env GOOS)"
host_arch="$(env -u GOARCH go env GOARCH)"
GOOS="${GOOS:-$host_os}"
GOARCH="${GOARCH:-$host_arch}"

suffix=""
if [ "$GOOS" = "windows" ]; then
  suffix=".exe"
fi
OUT="${OUT:-$root/bin/bicep-ext-k8smanifest$suffix}"
ldflags="-s -w -X bicep-ext-k8smanifest/internal/version.Version=$EXT_VERSION"

# Regenerate the Bicep type definitions from the Kubernetes OpenAPI swagger
# (committed under src/gen/; the binary embeds them). Pass -swagger to point
# at a different/newer swagger file. Host platform on purpose: the generator
# runs here (a cross-built one cannot exec), only the extension binary is
# targeted.
GOOS="$host_os" GOARCH="$host_arch" go run -C "$root/src" ./cmd/gentypes -version "$EXT_VERSION"

# -s -w strips the symbol table and DWARF debug info: the binary is a
# distribution artifact (~5 MB smaller, nothing to debug in it), which keeps
# the OCI blob upload comfortably small. -X stamps the version (the variable,
# not a constant, so the linker can rewrite it).
go build -C "$root/src" -ldflags="$ldflags" -o "$OUT" .

# Sanity-check the stamp so a wrong -X path fails here, not at publish time.
# Cross builds cannot run their own binary; the host build covers the stamp.
if [ "$GOOS/$GOARCH" = "$host_os/$host_arch" ]; then
  stamped=$("$OUT" --version)
  if [ "$stamped" != "$EXT_VERSION" ]; then
    echo "ERROR: binary stamped '$stamped', expected '$EXT_VERSION'" >&2
    exit 1
  fi
else
  echo "==> cross build $GOOS/$GOARCH: skipping the --version stamp check (the host build covers it)"
fi

if [ "${SKIP_LOCAL_PACKAGE:-0}" = "1" ]; then
  echo "==> SKIP_LOCAL_PACKAGE=1: skipping the local package"
  exit 0
fi

if [ "$GOOS/$GOARCH" != "$host_os/$host_arch" ]; then
  echo "==> cross build $GOOS/$GOARCH: skipping the local package (consume the host build, or use scripts/release-build.sh)"
  exit 0
fi

bicep_cmd=$(command -v bicep || echo "$HOME/.azure/bin/bicep")

bin_flag="--bin-${host_os}-x64"
case "$host_os/$host_arch" in
  linux/amd64)    bin_flag="--bin-linux-x64" ;;
  linux/arm64)    bin_flag="--bin-linux-arm64" ;;
  darwin/amd64)   bin_flag="--bin-osx-x64" ;;
  darwin/arm64)   bin_flag="--bin-osx-arm64" ;;
  windows/amd64)  bin_flag="--bin-win-x64" ;;
  windows/arm64)  bin_flag="--bin-win-arm64" ;;
  *) echo "ERROR: no bicep package flag for host $host_os/$host_arch" >&2; exit 1 ;;
esac

"$bicep_cmd" publish-extension \
  "$bin_flag" "$OUT" \
  --target "$root/bin/bicep-ext-k8smanifest-pkg" \
  --force

echo "Built extension v$EXT_VERSION ($GOOS/$GOARCH) and published local package: $root/bin/bicep-ext-k8smanifest-pkg"
