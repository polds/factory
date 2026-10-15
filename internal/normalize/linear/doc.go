// Package linear normalizes Linear webhook bodies into the M4 event contract.
//
// Signature checks stay in ingress (T18, scheme linear: hex SHA-256 and a 60s
// webhookTimestamp). Normalize runs only on a body that has already verified.
// Zero events and a nil error means the delivery is ignorable. Malformed JSON,
// a truncated body, and an issue update whose diff cannot be read return an error.
//
// Mapped events:
//
//   - Issue action create → issue_created
//   - Issue action update with a changed assigneeId → issue_assigned
//   - Issue action update with changed labelIds or labels → issue_labeled
//   - Issue action update with a changed stateId → issue_state_changed
//   - Comment action create on an issue → comment_created
//   - AgentSessionEvent action created → agent_session_created
//
// One issue update can emit more than one update event, in the order above.
// thread.kind is linear_issue. thread.external_ref is "linear:" plus the issue
// id. A comment or agent session with no issue id is ignored. AgentSessionEvent
// action prompted is ignored; the contract has no separate event for it.
// Type and action strings are case-sensitive and match Linear's payload.
//
// subject.labels is every current label name, in data.labels order. On
// issue_labeled, names added by the update are the data.labels entries whose
// id is not in updatedFrom.labelIds. Ids removed by the update are
// updatedFrom.labelIds entries that are not in data.labelIds. The matcher
// reads that diff from raw; subject.labels stays the full current set.
//
// docs/07 filter keys that are not first-class subject fields are read from
// the raw provider body:
//
//   - team_ids: data.teamId, data.issue.teamId, or agentSession.issue.teamId
//   - project_ids: data.projectId
//   - state_ids: data.stateId, with the previous id in updatedFrom.stateId
//   - issue_ids: subject.id
//   - labels: subject.labels (see the issue_labeled diff above)
//   - assignee_ids: data.assigneeId, with the previous id in updatedFrom.assigneeId
//   - creator_ids: data.creatorId on issues; actor.provider_id on comments and sessions
//   - keywords: subject.title and subject.text
//
// mentioned_user_ids are left in the comment or description text. Linear issues
// have no code forge, so subject.repo is empty.
//
// Delivery and Event are a vendor of the M4 contract (internal/ingress and
// internal/event) so this package can land before T18. JSON object names follow
// docs/07. Rebase replaces the vendored types with those packages.
package linear
