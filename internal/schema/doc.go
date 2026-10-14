// Package schema publishes factory definition JSON Schemas and serves them
// without authentication.
//
// Documents live under schemas/<version>/*.schema.json. factoryd mounts
// NewHandler on the process (M3 T10); this package does not bind a port.
// Markdown frontmatter (agent.md, automation.md, scorer.md, SKILL.md) is
// validated offline (layer 2), not by JSON Schema.
package schema
