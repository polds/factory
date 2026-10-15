// Package linear creates and edits one issue comment through Linear's GraphQL API.
//
// CreateComment calls commentCreate and returns the new comment id. UpdateComment
// calls commentUpdate on that id. A work item keeps a single factory comment and
// edits it in place; this package does not store the id.
//
// The API key is sent verbatim as the Authorization header. Linear API keys are
// the header value itself. An OAuth token is sent the same way, so the caller
// includes a Bearer prefix when the credential is an OAuth token. Errors and
// String never include the key.
//
// Endpoint defaults to https://api.linear.app/graphql. Tests point it at an
// httptest server. Live Linear is not part of the M4 exit.
package linear
