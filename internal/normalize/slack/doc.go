// Package slack normalizes Slack Events API deliveries into canonical events.
//
// T18 has not landed internal/event or internal/ingress on main. Delivery and
// Event in contract.go are the M4 plan contract, with JSON names from docs/07.
// Rebase onto T18 by deleting contract.go and importing those packages.
//
// Channel type mapping for message events:
//
//	channel, group, or omitted → message_posted
//	im                         → message_im
//	mpim                       → message_mpim
//	app_home, dm               → message_dm
//
// app_mention, reaction_added, and member_joined_channel keep those names.
// An unknown event or channel type yields no events and no error.
//
// thread.external_ref is slack:<team>/<channel>/<thread_ts or ts>. A reply
// uses the root thread_ts, so it names the same thread as the root message.
// The envelope verification token is not copied into the event.
//
// Messages are dropped when they are this app's own post, or when the subtype
// is an edit (message_changed, message_deleted) or a join (channel_join,
// group_join). member_joined_channel is the listed join event and is kept.
// Own posts are those whose user matches the configured user id or an
// authorizations entry with is_bot, whose bot_id matches the configured bot
// id, or which carry a bot_id and no user.
package slack
