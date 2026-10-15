// Package webhooks is the custom-webhook ingress for factory definitions.
//
// Two routes accept deliveries. POST /api/v1/webhooks/{webhook_uid} is
// authMode token (Authorization: Bearer, compared in constant time) or
// authMode signature (a scheme from internal/ingress/sig). POST
// /api/v1/webhooks/u/{url_token} is authMode url_token: the path segment
// is the secret, the lookup key is its SHA-256, and the segment must match
// charset A-Za-z0-9-._~. The token is never written to a log line. Access
// logs must pass the request URI through RedactRequestURI, which replaces
// that path segment with "{redacted}". Handler does this when Config.Log
// is set.
//
// Verification runs on the raw body before any JSON parse. A failed verify
// is 401 and is not persisted. The body read is capped at 1 MiB (413,
// also not persisted). Trigger matching is not done here.
//
// deliveryIdHeader, when set, is the dedup id. It is at most 64 characters,
// a valid HTTP header name, and neither Authorization, Cookie, nor a
// signature header. When it is empty, the dedup id is
// hex(SHA-256(raw body)) + "/" + floor(unix seconds / 300). Identical
// bodies in the same 5-minute bucket are one delivery; the same body in a
// later bucket is a new delivery. The bucket clock is Config.Now.
//
// Apply syncs definition webhooks onto webhook_registrations, matched by
// name. authMode and signatureScheme do not change in place: the error
// names the delete-and-recreate rule (ImmutableRule) and unwraps to
// store.ErrImmutable. enabled false is stored, and the route then returns
// 410 before verify and before any delivery is persisted, including when
// the signing secret does not exist yet.
//
// Normalize emits one webhook/received event. raw is the JSON body.
// thread.external_ref is the webhook uid, which is what the webhook_ids
// matcher compares.
package webhooks
