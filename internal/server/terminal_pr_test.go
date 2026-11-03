package server

import (
	"errors"
	"testing"
)

func TestPullRequestMissingDecision(t *testing.T) {
	readErr := errors.New("snapshot read")
	cases := []struct {
		name    string
		agentID string
		known   bool
		opted   bool
		n       int
		readErr error
		missing bool
		fail    bool
	}{
		{name: "opted in, no url", agentID: "agt", known: true, opted: true, missing: true},
		{name: "opted in, url recorded", agentID: "agt", known: true, opted: true, n: 1},
		{name: "opted in, read error", agentID: "agt", known: true, opted: true, readErr: readErr, missing: true},
		{name: "not opted in", agentID: "agt", known: true},
		{name: "not opted in, read error", agentID: "agt", known: true, readErr: readErr, fail: true},
		{name: "snapshot miss, named agent", agentID: "agt", missing: true},
		{name: "snapshot miss, named agent, read error", agentID: "agt", readErr: readErr, missing: true},
		{name: "snapshot miss, named agent, url recorded", agentID: "agt", n: 1},
		{name: "no agent", agentID: ""},
		{name: "no agent, read error", agentID: "", readErr: readErr, fail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			missing, err := pullRequestMissingDecision(tc.agentID, tc.known, tc.opted, tc.n, tc.readErr)
			if tc.fail {
				if !errors.Is(err, readErr) || missing {
					t.Fatalf("missing %v err %v", missing, err)
				}
				return
			}
			if err != nil || missing != tc.missing {
				t.Fatalf("missing %v err %v", missing, err)
			}
		})
	}
}
