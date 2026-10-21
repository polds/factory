package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLifecycleSSEReconnect(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := env.factoryFrames(t, ctx, "/api/v1/factory/m2/events", "")
	runID := env.startRun(t, "lifecycle reconnect "+lifecyclePlant)
	created := waitLife(t, frames, runID)
	if created.event != "run.created" {
		t.Fatalf("event %s data %s", created.event, created.data)
	}
	assertWireLifecycle(t, created, runID, "queued", "")
	if _, err := env.raw.Exec(context.Background(), `
		UPDATE runs SET status = 'idle', stage = 'building' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	idle := waitLife(t, frames, `"status":"idle"`)
	if idle.event != "run.status" || idle.id != created.id+1 {
		t.Fatalf("idle %+v created %d", idle, created.id)
	}
	assertWireLifecycle(t, idle, runID, "idle", "building")
	if !seqStored(t, env, runID, "run.status", idle.id) {
		t.Fatalf("stored factory_seq is not %d", idle.id)
	}
	cancel()

	again, stop := context.WithCancel(context.Background())
	defer stop()
	resumed := env.factoryFrames(t, again, "/api/v1/factory/m2/events", fmt.Sprintf("%d", created.id))
	againIdle := waitLife(t, resumed, `"status":"idle"`)
	if againIdle.id != idle.id || againIdle.event != "run.status" || againIdle.data != idle.data {
		t.Fatalf("resumed %+v want %+v", againIdle, idle)
	}
	expectNoLife(t, resumed, 300*time.Millisecond)
	stop()

	tail, stopTail := context.WithCancel(context.Background())
	defer stopTail()
	next := env.factoryFrames(t, tail, "/api/v1/factory/m2/events", fmt.Sprintf("%d", idle.id))
	if _, err := env.raw.Exec(context.Background(), `UPDATE runs SET status = 'succeeded' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	done := waitLife(t, next, `"status":"succeeded"`)
	if done.event != "run.status" || done.id != idle.id+1 {
		t.Fatalf("succeeded %+v idle %d", done, idle.id)
	}
	assertWireLifecycle(t, done, runID, "succeeded", "building")
	if strings.Contains(done.data, `"status":"idle"`) || strings.Contains(done.data, "run.created") {
		t.Fatalf("duplicate %s", done.data)
	}
}

func TestLifecycleRestart(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	ctx, cancel := context.WithCancel(context.Background())
	frames := env.factoryFrames(t, ctx, "/api/v1/factory/m2/events", "")
	runID := env.startRun(t, "lifecycle restart "+lifecyclePlant)
	created := waitLife(t, frames, runID)
	if created.event != "run.created" {
		t.Fatalf("event %s", created.event)
	}
	if _, err := env.raw.Exec(context.Background(), `
		UPDATE runs SET status = 'idle', stage = 'building' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	idle := waitLife(t, frames, `"status":"idle"`)
	if idle.id != created.id+1 || !seqStored(t, env, runID, "run.status", idle.id) {
		t.Fatalf("idle id %d created %d", idle.id, created.id)
	}
	cancel()
	env.restart(t)

	resumed, stop := context.WithCancel(context.Background())
	defer stop()
	frames = env.factoryFrames(t, resumed, "/api/v1/factory/m2/events", fmt.Sprintf("%d", created.id))
	again := waitLife(t, frames, `"status":"idle"`)
	if again.id != idle.id || again.event != idle.event || again.data != idle.data {
		t.Fatalf("after restart %+v want %+v", again, idle)
	}
	expectNoLife(t, frames, 300*time.Millisecond)
	if !seqStored(t, env, runID, "run.status", idle.id) {
		t.Fatal("factory_seq changed across restart")
	}
}

func TestLifecyclePerRunSeq(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "lifecycle seq "+lifecyclePlant)
	asg := w.readAssignment(t)
	env.waitStatus(t, runID, "running")
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"message.delta","text":"one"}`,
		`{"type":"message.delta","text":"two"}`,
	})
	before := env.eventRows(t, runID)
	if len(before) != 2 || before[0].seq != 1 || before[1].seq != 2 || before[0].typ != "message.delta" || before[1].typ != "message.delta" {
		t.Fatalf("rows %+v", before)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := env.factoryFrames(t, ctx, "/api/v1/agent/runs/"+runID+"/events?from_seq=0", "")
	first := waitLife(t, frames, "one")
	second := waitLife(t, frames, "two")
	if first.id != 1 || second.id != 2 || first.event != "message.delta" || second.event != "message.delta" {
		t.Fatalf("stream %s/%d %s/%d", first.event, first.id, second.event, second.id)
	}
	if _, err := env.raw.Exec(context.Background(), `UPDATE runs SET status = 'idle' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	expectNoLife(t, frames, 300*time.Millisecond)
	after := env.eventRows(t, runID)
	if len(after) != len(before) || after[0].seq != before[0].seq || after[1].seq != before[1].seq || after[0].typ != before[0].typ || after[1].typ != before[1].typ {
		t.Fatalf("after %+v before %+v", after, before)
	}
	var life int
	if err := env.raw.QueryRow(context.Background(), `
		SELECT count(*) FROM run_lifecycle_events WHERE run_id = $1`, runID).Scan(&life); err != nil {
		t.Fatal(err)
	}
	if life < 4 {
		t.Fatalf("lifecycle rows %d", life)
	}
	var leaked int
	if err := env.raw.QueryRow(context.Background(), `
		SELECT count(*) FROM run_events
		WHERE run_id = $1 AND type IN ('run.created', 'run.status')`, runID).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("per-run lifecycle rows %d", leaked)
	}
	token := asg.EnvVars["FACTORY_RUN_TOKEN"]
	var raw string
	if err := env.raw.QueryRow(context.Background(), `
		SELECT COALESCE(string_agg(payload_json::text, ''), '')
		FROM run_lifecycle_events WHERE run_id = $1`, runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if token != "" && strings.Contains(raw, token) {
		t.Fatal("lifecycle payload contains the run token")
	}
	if strings.Contains(raw, lifecyclePlant) {
		t.Fatal("lifecycle payload contains the prompt")
	}
}

const lifecyclePlant = "rt_SECRET_PLANT"

func waitLife(t *testing.T, frames <-chan sseFrame, needle string) sseFrame {
	t.Helper()
	deadline := time.After(4 * time.Second)
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatalf("stream closed before %s", needle)
			}
			if strings.Contains(frame.data, needle) || frame.event == needle {
				return frame
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", needle)
			return sseFrame{}
		}
	}
}

func expectNoLife(t *testing.T, frames <-chan sseFrame, wait time.Duration) {
	t.Helper()
	select {
	case frame, ok := <-frames:
		if ok {
			t.Fatalf("unexpected frame %s %s", frame.event, frame.data)
		}
	case <-time.After(wait):
	}
}

func assertWireLifecycle(t *testing.T, frame sseFrame, runID, status, stage string) {
	t.Helper()
	if strings.Contains(frame.data, lifecyclePlant) {
		t.Fatalf("wire contains a secret %s", frame.data)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(frame.data), &payload); err != nil {
		t.Fatal(err)
	}
	gid, ok := payload["gid"].(float64)
	if !ok || int64(gid) != frame.id {
		t.Fatalf("gid %v id %d", payload["gid"], frame.id)
	}
	want := map[string]any{
		"run_id": runID,
		"status": status,
		"gid":    gid,
	}
	if stage != "" {
		want["stage"] = stage
	}
	if len(payload) != len(want) {
		t.Fatalf("wire %s", frame.data)
	}
	for key, value := range want {
		if payload[key] != value {
			t.Fatalf("wire %s key %s = %v", frame.data, key, payload[key])
		}
	}
}

func seqStored(t *testing.T, env *env, runID, eventType string, seq int64) bool {
	t.Helper()
	var got int64
	err := env.raw.QueryRow(context.Background(), `
		SELECT factory_seq FROM run_lifecycle_events
		WHERE run_id = $1 AND type = $2 AND payload_json->>'status' = 'idle'`,
		runID, eventType).Scan(&got)
	if err != nil {
		t.Fatal(err)
	}
	return got == seq
}
