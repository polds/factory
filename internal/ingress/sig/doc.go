// Package sig verifies provider webhook signatures.
//
// Every comparison uses hmac.Equal. Timestamped schemes reject a skew
// beyond their tolerance in either direction. Verify returns before the
// caller parses the body; Linear reads webhookTimestamp only after the
// HMAC matches.
package sig
