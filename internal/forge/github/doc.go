// Package github is the GitHub App client used by the factory.
//
// It signs an app JWT (RS256, issued 60 seconds in the past, expiring in
// 10 minutes), looks up an installation by repository, and mints installation
// tokens scoped to repositories and permissions. Tokens stay in memory until
// five minutes before expiry and are never serialized. String, fmt, and slog
// output redact the secret.
package github
