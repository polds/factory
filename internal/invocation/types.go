package invocation

// Principal is the authenticated caller. It is never taken from the JSON body.
type Principal struct {
	Kind  string `json:"kind"`
	Actor string `json:"actor"`
	Allow bool   `json:"-"`
}

// Profile selects reviewed defaults. Only name is allowed on the wire.
type Profile struct {
	Name string `json:"name"`
}

// Target is an optional repository input.
type Target struct {
	Repository  string `json:"repository,omitempty"`
	Ref         string `json:"ref,omitempty"`
	PullRequest int    `json:"pullRequest,omitempty"`
}

// Correlation binds replies without defining occurrence identity.
type Correlation struct {
	ConversationID string `json:"conversationId,omitempty"`
	ReplyTo        string `json:"replyTo,omitempty"`
	ThreadRef      string `json:"threadRef,omitempty"`
}

// Request is the in-process Invoke input after HTTP decoding and validation.
type Request struct {
	DefinitionRevision  string
	Profile             Profile
	Target              *Target
	Inputs              map[string]any
	SourceOccurrenceKey string
	Correlation         *Correlation
	Intent              string
	Priority            string
	ExecuteTargetGrant  bool
	OverlapPolicy       string
	ScheduleID          string
	DueSlot             string
}

// Receipt is returned from Invoke and persisted for replay.
type Receipt struct {
	InvocationID        string       `json:"invocationId"`
	WorkItemID          string       `json:"workItemId,omitempty"`
	FactoryUID          string       `json:"factoryUid"`
	DefinitionRevision  string       `json:"definitionRevision"`
	DefinitionDigest    string       `json:"definitionDigest"`
	ProfileName         string       `json:"profileName"`
	ProfileDigest       string       `json:"profileDigest"`
	PolicyDigest        string       `json:"policyDigest"`
	PinnedHeadSHA       string       `json:"pinnedHeadSha,omitempty"`
	PinnedBaseSHA       string       `json:"pinnedBaseSha,omitempty"`
	State               string       `json:"state"`
	Outcome             string       `json:"outcome,omitempty"`
	CountsAsCompleted   bool         `json:"countsAsCompletedScan"`
	SourceOccurrenceKey string       `json:"sourceOccurrenceKey"`
	CanonicalDigest     string       `json:"canonicalDigest"`
	Intent              string       `json:"intent"`
	Priority            string       `json:"priority"`
	Links               ReceiptLinks `json:"links"`
	Replay              bool         `json:"-"`
}

// ReceiptLinks are stable GET paths for the receipt and result.
type ReceiptLinks struct {
	Self   string `json:"self"`
	Result string `json:"result,omitempty"`
}

const (
	StateAccepted         = "accepted"
	StateSkippedOverlap   = "skipped_overlap"
	OutcomeSkippedOverlap = "skipped_overlap"
)
