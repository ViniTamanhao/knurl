// Package schemas embeds knurl's frozen JSON Schema Draft 2020-12 contracts.
package schemas

import _ "embed"

// ManifestJSON holds the embedded manifest.schema.json specification.
//
//go:embed manifest.schema.json
var ManifestJSON []byte

// TestsJSON holds the embedded tests.schema.json specification.
//
//go:embed tests.schema.json
var TestsJSON []byte
