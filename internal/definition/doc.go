// Package definition loads a local factory tree and validates it.
//
// LoadDir reads factory.yaml, agents, runners, routers, skills, pipeline,
// automations, scorers, webhooks, and benchmarks. Unknown keys are errors.
//
// SlackIntegration.AllowedUserIDs lists Slack user ids that may continue a
// thread. An absent or empty list denies every Slack user.
package definition
