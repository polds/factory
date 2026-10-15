// Package slack posts and edits Slack messages with a bot token.
//
// Client implements Poster against chat.postMessage and chat.update.
// The API base URL is injectable so tests can use a fake Slack server.
// An HTTP 429 waits for the Retry-After delay and tries again.
// A missing or unreadable Retry-After waits one second. After the retry
// budget the error is returned. ok:false responses surface the Slack error
// code. The bot token is never included in errors or in the client's
// printed form.
package slack
