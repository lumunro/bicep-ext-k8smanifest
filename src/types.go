// Copyright 2026 lumunro
// SPDX-License-Identifier: MIT

// Package main hosts the generated Bicep type definitions.
//
// The type files (gen/types.json, gen/index.json) are produced by
// cmd/gentypes from the Kubernetes OpenAPI swagger file (see
// scripts/build.sh, which always regenerates them before the build).
// They are committed so a bare `go build` works without the swagger, and so
// type changes are visible in diffs. Regenerate with:
//
//	go run -C src ./cmd/gentypes
package main

import _ "embed"

const typesFileName = "types.json"

//go:embed gen/types.json
var embeddedTypes string

//go:embed gen/index.json
var embeddedIndex string

// loadTypeFiles returns the generated Bicep type definitions served to the
// Bicep IDE via GetTypeFiles: the type index (resources keyed by
// "<group>/<Kind>@<version>", fallback resource, extension settings) and the
// type file it references.
//
// The index entries and the extension configuration reference are
// CrossFileTypeReferences carrying the types.json relative path — a bare
// "#/N" makes the Bicep CLI's packer treat the relative path as empty and
// fail with a misleading error.
func loadTypeFiles() (indexContent string, typeFiles map[string]string) {
	return embeddedIndex, map[string]string{typesFileName: embeddedTypes}
}
