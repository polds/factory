// Package automation turns a normalized provider event into work.
//
// Handle compiles the applied definition's triggers, cached by digest, and
// matches them with internal/trigger. A new thread creates one work item and
// enqueues the first pipeline stage once per matching automation. Creating
// the work item and binding its external ref is one store transaction, and
// the intake slot for each automation is durable. A re-claimed delivery
// enqueues a missing first stage. It does not open another work item and it
// does not append context. A later delivery on an already-opened thread
// follows the continuation rules.
//
// Continuation is a Slack thread reply when autoRespondToThreadReplies is
// set, or a GitHub comment on a bound issue or pull request. Bot actors are
// ignored on intake and on replies. Allow fails closed: an empty Slack
// allowlist admits no Slack user, and a nil GitHub check admits no GitHub
// commenter. T30 supplies both. definition.SlackIntegration has no
// allowedUserIds field yet; T30 still has to inject the ids.
//
// An eligible reply calls ResumingAnswerer. That is (*server.Server).AnswerInput
// from T27, not (*store.Store).AnswerInputRequest. ErrNotFound means there
// is no question. Handle then starts a building-stage follow-up when a
// terminal handoff has an open pull request, or appends the event for the
// next stage. An already-stored answer is passed to the answerer again so
// a retry resumes a parked run instead of following up.
//
// AppendContext writes work_items.body. server.stagePrompt passes that
// column into the next stage prompt.
//
// A stage change is re-entered into Handle as factory/work_item_stage_changed.
// The cause chain includes the automations stored for the work item, so the
// guard holds in a process that did not create the item. An automation does
// not handle an event whose cause chain already includes it.
//
// T30 wires Store and Engine.Answer. Store methods must not call Handle.
// The matcher cache is the only state under the engine lock. Store calls
// are not made while that lock is held.
package automation
