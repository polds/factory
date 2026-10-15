// Package definition loads a local factory tree and validates it.
//
// LoadDir reads factory.yaml, agents, runners, routers, skills, and pipeline.
// Automations, scorers, and webhooks are loaded and validated; their engines
// are later milestones. Unknown keys are errors.
//
// SlackIntegration.AllowedUserIDs lists Slack user ids that may continue a
// thread. An absent or empty list denies every Slack user.
package definition
