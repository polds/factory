// Package schemas embeds the published JSON Schema documents.
// factoryd serves this tree when no schemas directory is on disk.
package schemas

import "embed"

// FS is rooted at the schemas directory: one subdirectory per version.
//
//go:embed v1alpha1/*.json
var FS embed.FS
