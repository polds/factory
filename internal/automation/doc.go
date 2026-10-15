// Package automation turns a normalized provider event into work.
//
// Handle compiles the applied definition's triggers, cached by digest, and
// matches them with internal/trigger. A new thread creates one work item and
// enqueues the first pipeline stage once per matching automation. The same
// automation and delivery is idempotent for the life of this process. An
// existing thread follows the continuation rules: answer a pending question,
// start a building-stage follow-up when a terminal handoff has an open pull
// request, or keep the event for the next stage run.
//
// A stage change is re-entered into Handle as factory/work_item_stage_changed.
// An automation does not handle an event whose cause chain already includes
// that automation.
//
// T30 wires Store to the M4 store. Store methods must not call Handle.
package automation
