// Package handoff builds the prompt a child stage run receives (M3 D4).
//
// Each pipeline stage is a new run. BuildPrompt packages the work item, the
// agent instructions, and prior stage outputs into a deterministic
// FACTORY_TASK_PROMPT. Resolved environment values and tokens are redacted
// and are never copied into that prompt.
//
// NewConversationID returns conv_ plus the same 26-character Crockford
// identifier used for run ids.
package handoff
