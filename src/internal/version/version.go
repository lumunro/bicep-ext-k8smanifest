// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

// Package version is the single source of truth for the extension version.
//
// Version is a variable (not a constant) so distribution builds can stamp it
// at link time: scripts/build.sh passes
//
//	-ldflags "-X bicep-ext-k8smanifest/internal/version.Version=<EXT_VERSION>"
//
// and the same EXT_VERSION value is passed to cmd/gentypes (-version), which
// stamps gen/index.json's settings.version. The scripts also use it as the
// OCI tag, so the published tag, the binary stamp, and the generated type
// index all agree by construction.
package version

// Version holds the extension version. It defaults to "dev" for un-stamped
// development builds (go run, go test); distribution builds override it via
// -ldflags -X (see the package comment).
var Version = "dev"
