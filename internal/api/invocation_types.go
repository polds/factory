package api

import (
	"context"
	"errors"
)

// InvocationBackend serves POST/GET invocations and result retrieval.
type InvocationBackend interface {
	Invoke(ctx context.Context, factoryUID string, req InvocationRequest, principal InvocationPrincipal) (InvocationReceipt, error)
	GetInvocation(ctx context.Context, factoryUID, invocationID string) (InvocationReceipt, error)
	GetInvocationResult(ctx context.Context, factoryUID, invocationID string, principal InvocationPrincipal) (InvocationResult, error)
	PublishInvocationResult(ctx context.Context, factoryUID, invocationID string, principal InvocationPrincipal, rep InvocationResultPublish) error
}

// InvocationPrincipal is the authenticated caller for Invoke.
type InvocationPrincipal struct {
	Kind  string `json:"kind"`
	Actor string `json:"actor"`
}

// InvocationRequest mirrors the public JSON body without principal.
type InvocationRequest struct {
	DefinitionRevision  string             `json:"definitionRevision,omitempty"`
	Profile             *InvocationProfile `json:"profile,omitempty"`
	Target              *InvocationTarget  `json:"target,omitempty"`
	Inputs              map[string]any     `json:"inputs,omitempty"`
	SourceOccurrenceKey string             `json:"sourceOccurrenceKey"`
	Correlation         *InvocationCorr    `json:"correlation,omitempty"`
	Intent              string             `json:"intent"`
	Priority            string             `json:"priority"`
}

type InvocationProfile struct {
	Name string `json:"name"`
}

type InvocationTarget struct {
	Repository  string `json:"repository,omitempty"`
	Ref         string `json:"ref,omitempty"`
	PullRequest int    `json:"pullRequest,omitempty"`
}

type InvocationCorr struct {
	ConversationID string `json:"conversationId,omitempty"`
	ReplyTo        string `json:"replyTo,omitempty"`
	ThreadRef      string `json:"threadRef,omitempty"`
}

// InvocationReceipt is the HTTP receipt shape.
type InvocationReceipt struct {
	InvocationID        string                 `json:"invocationId"`
	WorkItemID          string                 `json:"workItemId,omitempty"`
	FactoryUID          string                 `json:"factoryUid"`
	DefinitionRevision  string                 `json:"definitionRevision"`
	DefinitionDigest    string                 `json:"definitionDigest"`
	ProfileName         string                 `json:"profileName,omitempty"`
	ProfileDigest       string                 `json:"profileDigest,omitempty"`
	PolicyDigest        string                 `json:"policyDigest,omitempty"`
	PinnedHeadSHA       string                 `json:"pinnedHeadSha,omitempty"`
	PinnedBaseSHA       string                 `json:"pinnedBaseSha,omitempty"`
	State               string                 `json:"state"`
	Outcome             string                 `json:"outcome,omitempty"`
	CountsAsCompleted   bool                   `json:"countsAsCompletedScan"`
	SourceOccurrenceKey string                 `json:"sourceOccurrenceKey"`
	CanonicalDigest     string                 `json:"canonicalDigest"`
	Intent              string                 `json:"intent"`
	Priority            string                 `json:"priority"`
	Links               InvocationReceiptLinks `json:"links"`
	Replay              bool                   `json:"-"`
}

type InvocationReceiptLinks struct {
	Self   string `json:"self"`
	Result string `json:"result,omitempty"`
}

// InvocationResult is the published report product.
type InvocationResult struct {
	Summary        string `json:"summary"`
	CoverageStatus string `json:"coverageStatus"`
	FindingsCount  int    `json:"findingsCount"`
	Report         string `json:"report,omitempty"`
}

// InvocationResultPublish is the write path for reports.
type InvocationResultPublish struct {
	Summary        string   `json:"summary"`
	CoverageStatus string   `json:"coverageStatus"`
	FindingsCount  int      `json:"findingsCount"`
	Report         string   `json:"report"`
	ArtifactPaths  []string `json:"artifactPaths,omitempty"`
}

// ErrInvocationConflict is returned for digest mismatch on the same occurrence key.
var ErrInvocationConflict = errors.New("invocation_conflict")

// ErrInvocationRejected carries an admission failure code for problem+json.
type ErrInvocationRejected struct {
	Code   string
	Status int
}

func (e ErrInvocationRejected) Error() string { return e.Code }
