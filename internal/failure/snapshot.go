package failure

import "encoding/json"

// snapshot is the whitelisted slice of executions.failure_details_json.
// Free-text fields (message, names, logs) are not decoded, so they cannot
// reach Summary or Evidence.
type snapshot struct {
	podPhase      string
	podReason     string
	podConditions []string
	termReason    string
	waitReason    string
	state         string
	normalized    *int
	signal        int
	jobConditions []string
	eventReasons  []string
}

// codedReason is one whitelisted reason field. Message text is not decoded.
type codedReason struct {
	Reason string `json:"reason"`
}

type snapshotDoc struct {
	Kubernetes *struct {
		Pod *struct {
			Phase      string        `json:"phase"`
			Reason     string        `json:"reason"`
			Conditions []codedReason `json:"conditions"`
		} `json:"pod"`
		Job *struct {
			Conditions []codedReason `json:"conditions"`
		} `json:"job"`
		Container *struct {
			State              string `json:"state"`
			WaitingReason      string `json:"waiting_reason"`
			TerminationReason  string `json:"termination_reason"`
			NormalizedExitCode *int   `json:"normalized_exit_code"`
			Signal             *int   `json:"signal"`
		} `json:"container"`
		Events []codedReason `json:"events"`
	} `json:"kubernetes"`
}

func parseSnapshot(raw json.RawMessage) snapshot {
	if len(raw) == 0 {
		return snapshot{}
	}
	var doc snapshotDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Kubernetes == nil {
		return snapshot{}
	}
	k := doc.Kubernetes
	var out snapshot
	if k.Pod != nil {
		out.podPhase = k.Pod.Phase
		out.podReason = k.Pod.Reason
		out.podConditions = reasonCodes(k.Pod.Conditions)
	}
	if k.Job != nil {
		out.jobConditions = reasonCodes(k.Job.Conditions)
	}
	if k.Container != nil {
		out.state = k.Container.State
		out.waitReason = k.Container.WaitingReason
		out.termReason = k.Container.TerminationReason
		out.normalized = k.Container.NormalizedExitCode
		if k.Container.Signal != nil && *k.Container.Signal > 0 {
			out.signal = *k.Container.Signal
		}
	}
	out.eventReasons = reasonCodes(k.Events)
	return out
}

func reasonCodes(in []codedReason) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, c := range in {
		if c.Reason != "" {
			out = append(out, c.Reason)
		}
	}
	return out
}

func (s snapshot) hasToken(token string) bool {
	if s.termReason == token || s.waitReason == token || s.podReason == token {
		return true
	}
	for _, r := range s.podConditions {
		if r == token {
			return true
		}
	}
	for _, r := range s.jobConditions {
		if r == token {
			return true
		}
	}
	for _, r := range s.eventReasons {
		if r == token {
			return true
		}
	}
	return false
}

func (s snapshot) oom() bool {
	return s.hasToken("OOMKilled") || s.hasToken("OOMKilling")
}

func (s snapshot) evicted() bool {
	return s.hasToken("Evicted")
}
