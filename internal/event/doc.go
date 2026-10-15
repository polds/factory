// Package event is the canonical provider event (docs/07, Normalized events).
//
// A normalizer maps one verified ingress delivery onto zero or more Events.
// Zero events means the delivery is ignorable. Trigger matching reads these
// fields and does not see the provider payload except through Raw.
package event
