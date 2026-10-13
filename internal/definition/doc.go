// Package definition loads a local factory tree and validates the M2 subset.
//
// LoadDir reads factory.yaml, agents/<slug>/agent.md, and runners/*.yaml.
// Pipeline, routers, scorers, webhooks, and automations are recognized and
// reported as warnings; they are not applied in M2.
package definition
