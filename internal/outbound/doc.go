// Package outbound keeps one thread per work item on Slack, GitHub, and Linear.
//
// T30 constructs a Notifier and calls Notify for run and work-item events.
// Notify never returns a provider error: delivery is at-least-once, and a
// failed post does not fail the run. A canceled context does return, so
// shutdown can stop the caller.
//
// Each work item has one thread row per provider (Thread). Slack posts one
// root and then edits one status message in the thread. GitHub keeps one
// marker comment and one check run, both edited in place. Linear keeps one
// comment and edits it. Question, pull-request, and done events also post a
// new thread reply. Status edits are coalesced to at most one per two
// seconds per message. Each attempt backs off exponentially, at most eight
// tries, and never waits longer than five minutes.
//
// Slack posts for a GitHub-originated item use Config.SlackChannel, copied
// from definition.SlackIntegration.Channel (json channel). T30 also sets
// Notice.Channel from the live definition so a later sync is visible
// without rebuilding the notifier. Every post names the agent and the model
// and links the run under Config.ExternalURL (FACTORY_EXTERNAL_URL).
//
// When Config.Refs, Notice.SlackTeam, and Notice.ConversationID are set, the
// Slack root is bound as slack:<team>/<channel>/<root ts> so a reply
// continues the work item. The GitHub issue ref is bound by intake, not here.
//
// GitHub and Slack client values must not be formatted or logged. Notifier
// implements fmt.Formatter and slog.LogValuer so a log of the notifier does
// not walk those clients. Installation tokens are passed to the forge client
// and are not written to the log.
//
// Signed outbound webhooks (standard_webhooks for run.* and work_item.*) are
// an M4 follow-up. They are not part of the M4 exit, and this package does
// not send them.
package outbound
