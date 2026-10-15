// Package github normalizes a verified GitHub webhook delivery into canonical
// events (docs/07). It does not verify signatures or talk to the GitHub API.
//
// X-GitHub-Event and action map onto docs/07 names:
//
//	issues.opened|assigned|labeled     issue_created|assigned|labeled
//	issue_comment.created              issue_commented, or pull_request_commented
//	                                   when the issue is a pull request
//	pull_request.opened|closed|merged issue pull_request_opened|_closed|_merged
//	  reopened|ready_for_review|labeled|assigned|synchronize|review_requested
//	  enqueued|dequeued                pull_request_ready|_labeled|_assigned|
//	                                   _synchronized|_review_requested|
//	                                   _enqueued|_dequeued
//	pull_request_review.submitted      pull_request_review_submitted
//	push                               push
//	check_run.rerequested              check_run_rerequested
//	check_suite.completed|rerequested  check_suite_completed|_rerequested
//	workflow_run.completed             workflow_run_completed
//	installation*                      installation_changed
//
// A closed pull request with merged=true is pull_request_merged and not also
// pull_request_closed. Any other event or action returns zero events and no
// error.
//
// thread.external_ref for an issue or pull request is owner/repo#N (kind
// github_issue). Push uses owner/repo@<git ref> (kind github_push). A check
// run, check suite, or workflow run uses owner/repo#N when it names exactly
// one pull request, and owner/repo@<head sha> otherwise.
// installation_changed uses kind github_installation and external_ref
// installation:<id>. subject.repo is set only when that payload names exactly
// one repository.
//
// subject.labels lists every current label on the issue or pull request.
// For labeled actions, raw.label.name is the label that was added. Trigger
// matchers select issue_labeled and pull_request_labeled by that added name,
// not by membership in subject.labels.
//
// issue_mentioned and pull_request_mentioned are not GitHub deliveries. When
// Normalizer.AppSlug is set, a new body that contains @<app-slug> (optional
// [bot] suffix, case-insensitive, token boundaries) adds the mentioned event
// beside the primary one. That applies to issues.opened, issue_comment.created,
// pull_request.opened, and pull_request_review.submitted. Later actions such
// as labeled do not scan the existing body, so a label does not re-fire a
// mention. Package Normalize uses an empty slug and never emits mentions.
//
// Bot senders (type Bot) keep a [bot] handle. A login that does not already
// contain [bot] gets the suffix.
//
// Event and Delivery mirror the M4 contract for internal/event and
// internal/ingress. T18 had not landed those packages when this package was
// written; a rebase onto T18 should switch the signatures to those types
// when the JSON shape matches.
package github
