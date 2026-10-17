package server

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// kubernetesSnapshot is the latest execution's failure_details_json, reduced
// to a fixed whitelist. Names, uids, and free-text messages are not decoded.
func (s *Server) kubernetesSnapshot(ctx context.Context, runID string) (map[string]string, error) {
	if s.db == nil || runID == "" {
		return nil, nil
	}
	var raw []byte
	err := s.db.QueryRow(ctx, `
		SELECT failure_details_json
		FROM executions
		WHERE run_id = $1
		ORDER BY attempt DESC
		LIMIT 1`, runID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return whitelistSnapshot(raw), nil
}

type snapshotReason struct {
	Reason string `json:"reason"`
}

type snapshotDoc struct {
	Kubernetes *struct {
		ObservedAt time.Time `json:"observed_at"`
		Truncated  bool      `json:"truncated"`
		Pod        *struct {
			Phase      string           `json:"phase"`
			Reason     string           `json:"reason"`
			Conditions []snapshotReason `json:"conditions"`
		} `json:"pod"`
		Job *struct {
			Conditions []snapshotReason `json:"conditions"`
		} `json:"job"`
		Container *struct {
			State              string `json:"state"`
			WaitingReason      string `json:"waiting_reason"`
			TerminationReason  string `json:"termination_reason"`
			RawExitCode        *int   `json:"raw_exit_code"`
			NormalizedExitCode *int   `json:"normalized_exit_code"`
			Signal             *int   `json:"signal"`
		} `json:"container"`
		Events []snapshotReason `json:"events"`
	} `json:"kubernetes"`
}

func whitelistSnapshot(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var doc snapshotDoc
	if json.Unmarshal(raw, &doc) != nil || doc.Kubernetes == nil {
		return nil
	}
	k := doc.Kubernetes
	out := map[string]string{}
	put := func(key, val string) {
		if val != "" {
			out[key] = val
		}
	}
	if k.Pod != nil {
		put("pod.phase", k.Pod.Phase)
		put("pod.reason", k.Pod.Reason)
		put("pod.conditions", joinSnapshotReasons(k.Pod.Conditions))
	}
	if k.Job != nil {
		put("job.conditions", joinSnapshotReasons(k.Job.Conditions))
	}
	if k.Container != nil {
		c := k.Container
		put("container.state", c.State)
		put("container.waiting_reason", c.WaitingReason)
		put("container.termination_reason", c.TerminationReason)
		if c.RawExitCode != nil {
			out["container.raw_exit_code"] = strconv.Itoa(*c.RawExitCode)
		}
		if c.NormalizedExitCode != nil {
			out["container.normalized_exit_code"] = strconv.Itoa(*c.NormalizedExitCode)
		}
		if c.Signal != nil {
			out["container.signal"] = strconv.Itoa(*c.Signal)
		}
	}
	put("events", joinSnapshotReasons(k.Events))
	if !k.ObservedAt.IsZero() {
		out["observed_at"] = k.ObservedAt.UTC().Format(time.RFC3339)
	}
	if k.Truncated {
		out["truncated"] = "true"
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func joinSnapshotReasons(in []snapshotReason) string {
	if len(in) == 0 {
		return ""
	}
	parts := make([]string, 0, len(in))
	for _, item := range in {
		if item.Reason != "" {
			parts = append(parts, item.Reason)
		}
	}
	return strings.Join(parts, ",")
}
