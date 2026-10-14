// Package defsync clones a git-backed factory definition and publishes a
// validated tree.
//
// Sync does not apply resources. The caller applies the published directory
// only when Sync reports changed. A validation error, a non-production ref,
// or an unchanged commit leaves the last published tree in place, so a
// partial definition is never observable.
package defsync
