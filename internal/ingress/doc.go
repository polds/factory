// Package ingress accepts provider deliveries.
//
// The handler verifies the signature before it parses, bounds the body,
// redacts credential headers, and enqueues the delivery. Trigger matching
// stays off the request path: Dispatcher claims stored deliveries and calls
// the supplied function asynchronously.
package ingress
