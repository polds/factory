package github

// payload is the GitHub webhook fields this normalizer reads. Unknown fields
// stay available through Event.Raw.
type payload struct {
	Action       string          `json:"action"`
	Issue        *ghIssue        `json:"issue"`
	Comment      *ghComment      `json:"comment"`
	PullRequest  *ghPullRequest  `json:"pull_request"`
	Review       *ghReview       `json:"review"`
	Repository   *ghRepository   `json:"repository"`
	Sender       *ghUser         `json:"sender"`
	Installation *ghInstallation `json:"installation"`
	CheckRun     *ghCheckRun     `json:"check_run"`
	CheckSuite   *ghCheckSuite   `json:"check_suite"`
	WorkflowRun  *ghWorkflowRun  `json:"workflow_run"`
	Ref          string          `json:"ref"`
	After        string          `json:"after"`
	Commits      []ghCommit      `json:"commits"`
	HeadCommit   *ghCommit       `json:"head_commit"`
	Repositories []ghRepository  `json:"repositories"`
	ReposAdded   []ghRepository  `json:"repositories_added"`
}

type ghUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghIssue struct {
	ID          int64     `json:"id"`
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	CreatedAt   string    `json:"created_at"`
	UpdatedAt   string    `json:"updated_at"`
	Labels      []ghLabel `json:"labels"`
	PullRequest *ghPRLink `json:"pull_request"`
}

type ghComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	HTMLURL   string `json:"html_url"`
	CreatedAt string `json:"created_at"`
}

type ghRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type ghPullRequest struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	Merged    bool      `json:"merged"`
	UpdatedAt string    `json:"updated_at"`
	Labels    []ghLabel `json:"labels"`
	Head      ghRef     `json:"head"`
	Base      ghRef     `json:"base"`
}

type ghRepository struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    ghUser `json:"owner"`
}

type ghCommit struct {
	ID        string   `json:"id"`
	Message   string   `json:"message"`
	Timestamp string   `json:"timestamp"`
	URL       string   `json:"url"`
	Added     []string `json:"added"`
	Removed   []string `json:"removed"`
	Modified  []string `json:"modified"`
}

type ghReview struct {
	ID          int64  `json:"id"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	SubmittedAt string `json:"submitted_at"`
}

type ghPRLink struct {
	Number int   `json:"number"`
	Head   ghRef `json:"head"`
	Base   ghRef `json:"base"`
}

type ghCheckRun struct {
	ID           int64         `json:"id"`
	Name         string        `json:"name"`
	HeadSHA      string        `json:"head_sha"`
	HTMLURL      string        `json:"html_url"`
	StartedAt    string        `json:"started_at"`
	CompletedAt  string        `json:"completed_at"`
	PullRequests []ghPRLink    `json:"pull_requests"`
	CheckSuite   *ghCheckSuite `json:"check_suite"`
}

type ghCheckSuite struct {
	ID           int64      `json:"id"`
	HeadBranch   string     `json:"head_branch"`
	HeadSHA      string     `json:"head_sha"`
	UpdatedAt    string     `json:"updated_at"`
	PullRequests []ghPRLink `json:"pull_requests"`
}

type ghWorkflowRun struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	HeadBranch   string     `json:"head_branch"`
	HeadSHA      string     `json:"head_sha"`
	Conclusion   string     `json:"conclusion"`
	HTMLURL      string     `json:"html_url"`
	UpdatedAt    string     `json:"updated_at"`
	PullRequests []ghPRLink `json:"pull_requests"`
}

type ghInstallation struct {
	ID        int64  `json:"id"`
	Account   ghUser `json:"account"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
