package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/inference"
	"github.com/polds/factory/internal/store"
)

func TestRunSpendHardStop(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	ctx := context.Background()

	t.Run("w_units", func(t *testing.T) {
		env.srv.cfg.SpendPrice = mustPrice(t, workedPrice())
		fac, inv, runID, token := spendRoot(t, env, "spend:w")
		var grants []int64
		env.srv.reserveObserver = func(cents int64, status int) {
			if status == http.StatusOK {
				grants = append(grants, cents)
			}
		}
		t.Cleanup(func() { env.srv.reserveObserver = nil })
		gw, calls := scriptedModel(t, "0.015", "served-163", false, 8192)
		code, events := runSpendHarness(t, env, gw, runID, token, env.srv.cfg.SpendPrice, "do the thing", 1, 0)
		if code != harness.ExitOK {
			t.Fatalf("exit %d", code)
		}
		if calls() != 1 || len(grants) != 1 || grants[0] != 163 {
			t.Fatalf("calls %d grants %v", calls(), grants)
		}
		_ = gw
		if got := usageSum(t, events); got != 2 {
			t.Fatalf("usage cost %d", got)
		}
		if !strings.Contains(events, `"model_served":"served-163"`) {
			t.Fatal("model_served missing")
		}
		roll := rollupInvocation(t, env, inv)
		q := quarantineInvocation(t, env, inv)
		if roll != 2 || q != 0 || roll+q != int64(usageSum(t, events)) {
			t.Fatalf("rollup %d quarantine %d usage %d", roll, q, usageSum(t, events))
		}
		if taskStatus(t, env, fac.ID, runTask(t, env, runID)) == store.NativePaused {
			t.Fatal("priced call paused the root")
		}
	})

	t.Run("deny_26_dollar_call", func(t *testing.T) {
		env.srv.cfg.SpendPrice = mustPrice(t, overPrice())
		_, _, runID, token := spendRoot(t, env, "spend:26")
		var seen []int64
		var statuses []int
		env.srv.reserveObserver = func(cents int64, status int) {
			seen = append(seen, cents)
			statuses = append(statuses, status)
		}
		t.Cleanup(func() { env.srv.reserveObserver = nil })
		gw, calls := scriptedModel(t, "26", "served", false, 0)
		code, _ := runSpendHarness(t, env, gw, runID, token, env.srv.cfg.SpendPrice, "do the thing", 1, 0)
		if code != harness.ExitConfigInvalid {
			t.Fatalf("exit %d", code)
		}
		if calls() != 0 || len(seen) != 1 || seen[0] != 2600 || statuses[0] != http.StatusConflict {
			t.Fatalf("calls %d cents %v status %v", calls(), seen, statuses)
		}
	})

	t.Run("reserve_below_server_w", func(t *testing.T) {
		env.srv.cfg.SpendPrice = mustPrice(t, workedPrice())
		_, _, runID, token := spendRoot(t, env, "spend:below")
		var seen []int64
		var statuses []int
		env.srv.reserveObserver = func(cents int64, status int) {
			seen = append(seen, cents)
			statuses = append(statuses, status)
		}
		t.Cleanup(func() { env.srv.reserveObserver = nil })
		gw, calls := scriptedModel(t, "0.01", "served", false, 0)
		code, _ := runSpendHarness(t, env, gw, runID, token, mustPrice(t, oneCentPrice()), "do the thing", 1, 0)
		if code != harness.ExitConfigInvalid {
			t.Fatalf("exit %d", code)
		}
		if calls() != 0 || len(seen) != 1 || seen[0] != 1 || statuses[0] != http.StatusConflict {
			t.Fatalf("calls %d cents %v status %v", calls(), seen, statuses)
		}
	})

	t.Run("unpriced_fallback", func(t *testing.T) {
		env.srv.cfg.SpendPrice = mustPrice(t, unpriced())
		_, _, runID, token := spendRoot(t, env, "spend:unpriced")
		var seen int
		env.srv.reserveObserver = func(int64, int) { seen++ }
		t.Cleanup(func() { env.srv.reserveObserver = nil })
		gw, calls := scriptedModel(t, "0.01", "served", false, 0)
		code, _ := runSpendHarness(t, env, gw, runID, token, env.srv.cfg.SpendPrice, "do the thing", 1, 0)
		if code != harness.ExitConfigInvalid || calls() != 0 || seen != 0 {
			t.Fatalf("exit %d calls %d reserves %d", code, calls(), seen)
		}
		res := postSpend(t, env, runID, token, "reserve", `{"cents":163,"call_id":"directbelow","attempt_id":"1"}`)
		if res.StatusCode != http.StatusConflict || !bytes.Contains(res.Body, []byte(`"unpriced"`)) {
			t.Fatalf("direct reserve %d %s", res.StatusCode, res.Body)
		}
		if calls() != 0 {
			t.Fatal("unpriced reserve sent a model request")
		}
	})

	t.Run("prompt_over_cap", func(t *testing.T) {
		price := workedPrice()
		price.PromptByteCap = 8
		env.srv.cfg.SpendPrice = mustPrice(t, price)
		_, _, runID, token := spendRoot(t, env, "spend:oversize")
		var seen int
		env.srv.reserveObserver = func(int64, int) { seen++ }
		t.Cleanup(func() { env.srv.reserveObserver = nil })
		gw, calls := scriptedModel(t, "0.01", "served", false, 0)
		code, events := runSpendHarness(t, env, gw, runID, token, env.srv.cfg.SpendPrice, "do the thing", 1, 0)
		if code != harness.ExitConfigInvalid || calls() != 0 || seen != 0 || !strings.Contains(events, "prompt_over_cap") {
			t.Fatalf("exit %d calls %d reserves %d", code, calls(), seen)
		}
	})

	t.Run("no_cost_outcomes", func(t *testing.T) {
		env.srv.cfg.SpendPrice = mustPrice(t, workedPrice())
		for _, mode := range []string{"error", "timeout", "cancel", "nocost"} {
			t.Run(mode, func(t *testing.T) {
				_, _, runID, token := spendRoot(t, env, "spend:"+mode)
				var gw *httptest.Server
				var calls func() int
				var code int
				var events string
				switch mode {
				case "error":
					gw, calls = failingModel(t, http.StatusInternalServerError)
					code, events = runSpendHarness(t, env, gw, runID, token, env.srv.cfg.SpendPrice, "do the thing", 2, 0)
				case "nocost":
					gw, calls = scriptedModel(t, "", "served", true, 8192)
					code, events = runSpendHarness(t, env, gw, runID, token, env.srv.cfg.SpendPrice, "do the thing", 2, 0)
				case "timeout":
					gw, calls = hangingModel(t, nil)
					code, events = runSpendHarness(t, env, gw, runID, token, env.srv.cfg.SpendPrice, "do the thing", 2, 200*time.Millisecond)
				case "cancel":
					started := make(chan struct{})
					gw, calls = hangingModel(t, started)
					code, events = runCanceledHarness(t, env, gw, runID, token, env.srv.cfg.SpendPrice, started)
				default:
					t.Fatalf("mode %s", mode)
				}
				_ = gw
				if calls() != 1 {
					t.Fatalf("calls %d", calls())
				}
				if !strings.Contains(events, `"status":"missing_usage"`) {
					t.Fatalf("events missing missing_usage: %s", events)
				}
				if usageSum(t, events) != 163 {
					t.Fatalf("settled %d", usageSum(t, events))
				}
				if taskStatus(t, env, "", runTask(t, env, runID)) != store.NativePaused {
					t.Fatalf("root not paused, exit %d", code)
				}
			})
		}
	})

	t.Run("cost_over_reservation", func(t *testing.T) {
		env.srv.cfg.SpendPrice = mustPrice(t, workedPrice())
		fac, inv, runID, token := spendRoot(t, env, "spend:over")
		gw, calls := scriptedModel(t, "2", "served-over", true, 8192)
		_, events := runSpendHarness(t, env, gw, runID, token, env.srv.cfg.SpendPrice, "do the thing", 2, 0)
		if calls() != 1 {
			t.Fatalf("calls %d", calls())
		}
		if usageSum(t, events) != 200 {
			t.Fatalf("usage %d", usageSum(t, events))
		}
		q := quarantineInvocation(t, env, inv)
		roll := rollupInvocation(t, env, inv)
		if q != 37 || roll != 163 || roll+q != 200 {
			t.Fatalf("rollup %d quarantine %d", roll, q)
		}
		if taskStatus(t, env, fac.ID, runTask(t, env, runID)) != store.NativePaused {
			t.Fatal("over-reservation did not pause")
		}
	})

	t.Run("allowance_402", func(t *testing.T) {
		env.srv.cfg.SpendPrice = mustPrice(t, workedPrice())
		_, _, runID, token := spendRoot(t, env, "spend:402")
		gw, calls := failingModel(t, http.StatusPaymentRequired)
		_, events := runSpendHarness(t, env, gw, runID, token, env.srv.cfg.SpendPrice, "do the thing", 2, 0)
		if calls() != 1 {
			t.Fatalf("calls %d", calls())
		}
		if !strings.Contains(events, "backend_allowance_exhausted") || strings.Contains(events, "sk-") {
			t.Fatal("402 status missing or secret leaked")
		}
		if taskStatus(t, env, "", runTask(t, env, runID)) != store.NativePaused {
			t.Fatal("402 did not pause")
		}
	})

	t.Run("concurrent_ceiling", func(t *testing.T) {
		env.srv.cfg.SpendPrice = mustPrice(t, thousandPrice())
		fac, inv, seats := spendSeats(t, env, "spend:race", 3)
		var mu sync.Mutex
		grants := 0
		env.srv.reserveObserver = func(cents int64, status int) {
			mu.Lock()
			defer mu.Unlock()
			if status == http.StatusOK {
				if cents != 1000 {
					t.Errorf("grant %d", cents)
				}
				grants++
			}
		}
		t.Cleanup(func() { env.srv.reserveObserver = nil })
		gw, calls := scriptedModel(t, "10", "served-race", false, 1000)
		var wg sync.WaitGroup
		for _, seat := range seats {
			wg.Add(1)
			go func(runID, token string) {
				defer wg.Done()
				runSpendHarness(t, env, gw, runID, token, env.srv.cfg.SpendPrice, "do the thing", 1, 0)
			}(seat.runID, seat.token)
		}
		wg.Wait()
		mu.Lock()
		gotGrants := grants
		mu.Unlock()
		if calls() != 2 || gotGrants != 2 {
			t.Fatalf("calls %d grants %d", calls(), gotGrants)
		}
		if rollupInvocation(t, env, inv)+reservedInvocation(t, env, inv) > 2500 {
			t.Fatalf("ceiling exceeded rollup %d reserved %d", rollupInvocation(t, env, inv), reservedInvocation(t, env, inv))
		}
		_ = fac
		_ = ctx
	})

	t.Run("role_files_env", func(t *testing.T) {
		env.srv.cfg.SpendPrice = mustPrice(t, workedPrice())
		fac, agentID, itemID, inv := nativeLeaseFixture(t, env, "pipe", "spend:rolefiles", store.StagePlanning)
		tree := bundledProof()
		_, task, err := env.srv.store.OpenNativeTree(ctx, store.OpenTreeInput{
			FactoryID: fac.ID, InvocationID: inv, AgentID: agentID, WorkItemID: itemID,
			Stage: store.StagePlanning, Tree: tree, CeilingCents: 2500, Slots: 1, MaxLive: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.raw.Exec(ctx, `UPDATE invocations SET pinned_head_sha = $2 WHERE id = $1`, inv, strings.Repeat("d", 40)); err != nil {
			t.Fatal(err)
		}
		if _, err := env.raw.Exec(ctx, `UPDATE work_items SET title = 'Goal title', body = 'Goal body' WHERE id = $1`, itemID); err != nil {
			t.Fatal(err)
		}
		act, err := coordination.ClaimActivation(ctx, env.srv.store, task.ID, store.ClaimInput{AgentID: agentID, TaskID: task.ID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.srv.store.Lease(ctx, "default", time.Minute, 1); err != nil {
			t.Fatal(err)
		}
		exec := startSpendExec(t, env, act.RunID, "role-token")
		run, err := env.srv.store.GetRun(ctx, act.RunID)
		if err != nil {
			t.Fatal(err)
		}
		bound, ok := env.srv.resolveBound(ctx, env.srv.snapshot(), run.AgentID)
		if !ok {
			t.Fatal("binding")
		}
		taskEnv, err := env.srv.taskEnv(ctx, run, bound)
		if err != nil {
			t.Fatal(err)
		}
		if taskEnv[harness.EnvSpendURL] != env.base+"/api/v1/agent/runs/"+run.ID+"/spend" {
			t.Fatalf("spend url %q", taskEnv[harness.EnvSpendURL])
		}
		if !strings.Contains(taskEnv[harness.EnvSpendPrice], `"max_tokens":8192`) {
			t.Fatal("price was not handed to the harness")
		}
		_, _, plainRun, _ := spendRoot(t, env, "spend:plain")
		plainGot, err := env.srv.store.GetRun(ctx, plainRun)
		if err != nil {
			t.Fatal(err)
		}
		plainBound, ok := env.srv.resolveBound(ctx, env.srv.snapshot(), plainGot.AgentID)
		if !ok {
			t.Fatal("plain binding")
		}
		plainEnv, err := env.srv.taskEnv(ctx, plainGot, plainBound)
		if err != nil {
			t.Fatal(err)
		}
		if plainEnv[harness.EnvSpendURL] != "" || plainEnv[harness.EnvSpendPrice] != "" {
			t.Fatal("non role_files run received spend config")
		}
		_ = exec
	})
}

func TestHarnessSecretsNotInProc(t *testing.T) {
	bin := buildHarness(t)
	t.Run("no_reexec", func(t *testing.T) {
		hits := runSecretScan(t, bin, true, nil, "", "")
		if hits == 0 {
			t.Fatal("scanner missed a secret when re-exec was skipped")
		}
	})
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	t.Run("forged_token", func(t *testing.T) {
		env.srv.cfg.SpendPrice = mustPrice(t, workedPrice())
		_, _, runID, token := spendRoot(t, env, "spend:forged")
		for _, name := range []string{"reserve", "settle"} {
			missing := postSpend(t, env, runID, "", name, `{"cents":163,"call_id":"c1","attempt_id":"1","reservation_id":"nres_missing"}`)
			if missing.StatusCode != http.StatusUnauthorized || bytes.Contains(missing.Body, []byte(token)) {
				t.Fatalf("missing %s %d %s", name, missing.StatusCode, missing.Body)
			}
			forged := postSpend(t, env, runID, "forged-token-value", name, `{"cents":163,"call_id":"c1","attempt_id":"1","reservation_id":"nres_forged"}`)
			if forged.StatusCode != http.StatusUnauthorized || bytes.Contains(forged.Body, []byte("forged-token-value")) {
				t.Fatalf("forged %s %d %s", name, forged.StatusCode, forged.Body)
			}
		}
	})
	t.Run("reexec", func(t *testing.T) {
		env.srv.cfg.SpendPrice = mustPrice(t, workedPrice())
		_, _, runID, token := spendRoot(t, env, "spend:proc")
		hits := runSecretScan(t, bin, false, env, runID, token)
		if hits != 0 {
			t.Fatalf("secret hits %d", hits)
		}
	})
}

type spendSeatRun struct {
	runID string
	token string
}

func spendRoot(t *testing.T, env *env, key string) (store.Factory, string, string, string) {
	t.Helper()
	fac, inv, seats := spendSeats(t, env, key, 1)
	return fac, inv, seats[0].runID, seats[0].token
}

func spendSeats(t *testing.T, env *env, key string, n int) (store.Factory, string, []spendSeatRun) {
	t.Helper()
	ctx := context.Background()
	fac, agentID, itemID, inv := nativeLeaseFixture(t, env, "pipe", key, store.StagePlanning)
	rootID := openServerTree(t, env, fac.ID, inv, agentID, itemID, store.StagePlanning, 4)
	ids := []string{rootID}
	if n > 1 {
		lead, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
			FactoryID: fac.ID, InvocationID: inv, ParentTaskID: rootID,
			Assignee: "lead", IdempotencyKey: key + ":lead", JoinPolicy: store.JoinFailParent,
		})
		if err != nil {
			t.Fatal(err)
		}
		spec, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
			FactoryID: fac.ID, InvocationID: inv, ParentTaskID: lead.Child.ID,
			Assignee: "specialist-a", IdempotencyKey: key + ":spec", JoinPolicy: store.JoinFailParent,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, lead.Child.ID, spec.Child.ID)
	}
	if len(ids) < n {
		t.Fatalf("seats %d", len(ids))
	}
	ids = ids[:n]
	var seats []spendSeatRun
	var acts []store.Activation
	for _, id := range ids {
		act, err := coordination.ClaimActivation(ctx, env.srv.store, id, store.ClaimInput{AgentID: agentID, TaskID: id})
		if err != nil {
			t.Fatal(err)
		}
		acts = append(acts, act)
	}
	leased, err := env.srv.store.Lease(ctx, "default", time.Minute, len(acts))
	if err != nil || len(leased) != len(acts) {
		t.Fatalf("lease %d %+v %v", len(acts), leased, err)
	}
	for i, act := range acts {
		token := strings.ReplaceAll("tok-"+key+"-"+ids[i], ":", "-")
		token = strings.ReplaceAll(token, "_", "")
		startSpendExec(t, env, act.RunID, token)
		seats = append(seats, spendSeatRun{runID: act.RunID, token: token})
	}
	return fac, inv, seats
}

func startSpendExec(t *testing.T, env *env, runID, token string) store.Execution {
	t.Helper()
	exec, err := env.srv.store.StartExecution(context.Background(), runID, "spend-worker", tokenHash(token))
	if err != nil {
		t.Fatal(err)
	}
	return exec
}

func runSpendHarness(t *testing.T, env *env, gw *httptest.Server, runID, token, price, prompt string, turns int, timeout time.Duration) (int, string) {
	t.Helper()
	rt := t.TempDir()
	var stderr strings.Builder
	code := harness.Run(context.Background(), harness.Options{
		Args: []string{"--task-id", runID, "--model", "m"},
		Env: []string{
			"OPENAI_BASE_URL=" + gw.URL,
			"OPENAI_API_KEY=sk-test-key",
			"FACTORY_RUN_TOKEN=" + token,
			harness.EnvSpendURL + "=" + env.base + "/api/v1/agent/runs/" + runID + "/spend",
			harness.EnvSpendPrice + "=" + price,
			"FACTORY_SECRET_VALUES=sk-test-key\n" + token,
			"FACTORY_TASK_PROMPT=" + prompt,
			"FACTORY_MAX_TURNS=" + itoa(turns),
		},
		Workspace:   t.TempDir(),
		RuntimeDir:  rt,
		Stderr:      &stderr,
		ChatTimeout: timeout,
	})
	events, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(events), "sk-test-key") || strings.Contains(stderr.String(), "sk-test-key") || strings.Contains(string(events), token) || strings.Contains(stderr.String(), token) {
		t.Fatal("secret leaked into events or stderr")
	}
	return code, string(events)
}

func runCanceledHarness(t *testing.T, env *env, gw *httptest.Server, runID, token, price string, entered <-chan struct{}) (int, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := t.TempDir()
	done := make(chan int, 1)
	go func() {
		done <- harness.Run(ctx, harness.Options{
			Args: []string{"--task-id", runID, "--model", "m"},
			Env: []string{
				"OPENAI_BASE_URL=" + gw.URL,
				"OPENAI_API_KEY=sk-test-key",
				"FACTORY_RUN_TOKEN=" + token,
				harness.EnvSpendURL + "=" + env.base + "/api/v1/agent/runs/" + runID + "/spend",
				harness.EnvSpendPrice + "=" + price,
				"FACTORY_TASK_PROMPT=do the thing",
				"FACTORY_MAX_TURNS=2",
			},
			Workspace:  t.TempDir(),
			RuntimeDir: rt,
			Stderr:     io.Discard,
		})
	}()
	select {
	case <-entered:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("canceled request did not start")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("canceled harness did not exit")
	}
	events, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	return 0, string(events)
}

func scriptedModel(t *testing.T, cost, model string, tool bool, wantMax int) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if wantMax > 0 {
			var doc struct {
				MaxTokens int `json:"max_tokens"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil || doc.MaxTokens != wantMax {
				t.Errorf("max_tokens %d", doc.MaxTokens)
			}
		}
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if cost != "" {
			w.Header().Set("x-litellm-response-cost", cost)
		}
		w.Header().Set("Content-Type", "application/json")
		if tool && n == 1 {
			_, _ = io.WriteString(w, `{"model":"`+model+`","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"true\"}"}}]}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"`+model+`","choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

func failingModel(t *testing.T, status int) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":{"message":"no"}}`, status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

func hangingModel(t *testing.T, started chan struct{}) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		calls++
		mu.Unlock()
		if started != nil {
			select {
			case <-started:
			default:
				close(started)
			}
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
		case <-timer.C:
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

type spendHTTP struct {
	StatusCode int
	Body       []byte
}

func postSpend(t *testing.T, env *env, runID, token, action, body string) spendHTTP {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, env.base+"/api/v1/agent/runs/"+runID+"/spend:"+action, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return spendHTTP{StatusCode: res.StatusCode, Body: raw}
}

func usageSum(t *testing.T, events string) int {
	t.Helper()
	sum := 0
	for _, line := range strings.Split(events, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev harness.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "usage" {
			sum += ev.CostCents
		}
	}
	return sum
}

func rollupInvocation(t *testing.T, env *env, invocationID string) int64 {
	t.Helper()
	roll, err := env.srv.store.RollupSpent(context.Background(), invocationID)
	if err != nil {
		t.Fatal(err)
	}
	return roll["invocation"]
}

func quarantineInvocation(t *testing.T, env *env, invocationID string) int64 {
	t.Helper()
	var sum int64
	err := env.raw.QueryRow(context.Background(), `
		SELECT COALESCE(sum(q.excess_cents), 0)
		FROM budget_quarantine q
		JOIN budget_scopes s ON s.scope_id = q.scope_id
		WHERE s.invocation_id = $1 AND s.kind = 'invocation'`, invocationID).Scan(&sum)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func reservedInvocation(t *testing.T, env *env, invocationID string) int64 {
	t.Helper()
	var sum int64
	err := env.raw.QueryRow(context.Background(), `
		SELECT COALESCE(sum(reserved_cents), 0)
		FROM budget_scopes WHERE invocation_id = $1 AND kind = 'invocation'`, invocationID).Scan(&sum)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func runTask(t *testing.T, env *env, runID string) string {
	t.Helper()
	var id string
	err := env.raw.QueryRow(context.Background(), `
		SELECT task_id FROM native_activations WHERE run_id = $1 ORDER BY fence DESC LIMIT 1`, runID).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func taskStatus(t *testing.T, env *env, _, taskID string) string {
	t.Helper()
	var status string
	err := env.raw.QueryRow(context.Background(), `SELECT task_status FROM native_tasks WHERE id = $1`, taskID).Scan(&status)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func workedPrice() harness.SpendPrice {
	return harness.SpendPrice{
		PromptByteCap: 400000,
		MaxTokens:     8192,
		Models: []harness.ModelRate{
			{Name: "nous/claude-sonnet-5.5", InputCostPerToken: "0.000003", OutputCostPerToken: "0.000015", CacheCreationInputTokenCost: "0.00000375"},
			{Name: "fallback", InputCostPerToken: "0.000003", OutputCostPerToken: "0.000015"},
		},
	}
}

func overPrice() harness.SpendPrice {
	return harness.SpendPrice{
		PromptByteCap: 398976,
		MaxTokens:     8000,
		Models:        []harness.ModelRate{{Name: "high", InputCostPerToken: "0.00006", OutputCostPerToken: "0.00025"}},
	}
}

func oneCentPrice() harness.SpendPrice {
	return harness.SpendPrice{
		PromptByteCap: 400000,
		MaxTokens:     1,
		Models:        []harness.ModelRate{{Name: "tiny", InputCostPerToken: "0.00000001", OutputCostPerToken: "0.00000001"}},
	}
}

func thousandPrice() harness.SpendPrice {
	return harness.SpendPrice{
		PromptByteCap: 400000,
		MaxTokens:     1000,
		Models:        []harness.ModelRate{{Name: "flat", InputCostPerToken: "0", OutputCostPerToken: "0.01"}},
	}
}

func unpriced() harness.SpendPrice {
	return harness.SpendPrice{
		PromptByteCap: 400000,
		MaxTokens:     8192,
		Models:        []harness.ModelRate{{Name: "fallback", InputCostPerToken: "0.000003"}},
	}
}

func mustPrice(t *testing.T, price harness.SpendPrice) string {
	t.Helper()
	raw, err := json.Marshal(price)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func bundledProof() definition.NativeTree {
	tree := serverProofTree()
	tree.BundleMode = definition.BundleModeRoleFiles
	tree.SourceCommit = strings.Repeat("a", 40)
	tree.RosterSHA256 = strings.Repeat("b", 64)
	tree.RosterGitBlob = strings.Repeat("c", 40)
	for i := range tree.Personas {
		for _, name := range []string{"AGENTS.md", "HEARTBEAT.md", "SOUL.md", "TOOLS.md"} {
			tree.Personas[i].Files = append(tree.Personas[i].Files, definition.RoleFile{Name: name, Bytes: []byte("seat")})
		}
	}
	return tree
}

func buildHarness(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "factory-harness")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/factory-harness")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build harness: %v\n%s", err, out)
	}
	return bin
}

func runSecretScan(t *testing.T, bin string, noReexec bool, env *env, runID, token string) int {
	t.Helper()
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	needle := "z9" + hex.EncodeToString(entropy[:]) + "z9"
	key := "sk-" + needle
	if token == "" {
		token = "run-" + needle
	}
	var calls int
	var spendURL string
	var price string
	var mu sync.Mutex
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-litellm-response-cost", "0.01")
		if n == 1 {
			args := string(mustJSON(map[string]string{"command": scanCommand(needle, token, spendURL)}))
			body := mustJSON(map[string]any{
				"model": "served",
				"choices": []any{map[string]any{"message": map[string]any{
					"role": "assistant",
					"tool_calls": []any{map[string]any{
						"id": "c1", "type": "function",
						"function": map[string]any{"name": "bash", "arguments": args},
					}},
				}}},
			})
			_, _ = w.Write(body)
			return
		}
		_, _ = io.WriteString(w, `{"model":"served","choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	t.Cleanup(gw.Close)
	if env == nil {
		spend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, ":reserve") {
				_, _ = io.WriteString(w, `{"reservation_id":"nres_fake","cents":163,"w":163}`)
				return
			}
			_, _ = io.WriteString(w, `{"counted_cents":1,"quarantined_cents":0}`)
		}))
		t.Cleanup(spend.Close)
		spendURL = spend.URL + "/api/v1/agent/runs/local/spend"
		price = mustPrice(t, workedPrice())
		runID = "local"
	} else {
		spendURL = env.base + "/api/v1/agent/runs/" + runID + "/spend"
		price = env.srv.cfg.SpendPrice
	}
	ws := t.TempDir()
	rt := t.TempDir()
	cmdEnv := append(os.Environ(),
		"OPENAI_BASE_URL="+gw.URL,
		"OPENAI_API_KEY="+key,
		"FACTORY_RUN_TOKEN="+token,
		harness.EnvSpendURL+"="+spendURL,
		harness.EnvSpendPrice+"="+price,
		"FACTORY_SECRET_VALUES="+key+"\n"+token+"\n"+spendURL+"\n"+needle,
		inference.ExtraHeadersEnv+"=x-test-secret: "+needle,
		"FACTORY_TASK_PROMPT=scan",
		"FACTORY_MAX_TURNS=2",
		"FACTORY_RUNTIME_DIR="+rt,
	)
	if noReexec {
		cmdEnv = append(cmdEnv, harness.EnvNoReexec+"=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--task-id", runID, "--model", "m")
	cmd.Dir = ws
	cmd.Env = cmdEnv
	out, err := cmd.CombinedOutput()
	raw, readErr := os.ReadFile(filepath.Join(ws, "hits.txt"))
	if readErr != nil {
		t.Fatalf("hits: %v harness: %v\n%s", readErr, err, out)
	}
	n := 0
	if _, scanErr := fmtSscan(string(raw), &n); scanErr != nil {
		t.Fatalf("hits %q: %v", raw, scanErr)
	}
	return n
}

func scanCommand(needle, key, extra string) string {
	// Only the harness process this test started, this shell, and their
	// children. Reading another process's descriptors can block on a live
	// pipe or consume bytes that process still needs.
	return `hits=0
self=$$
parent=$(awk '/^PPid:/ { print $2 }' /proc/self/status)
pids="$self $parent"
if [ -r "/proc/$parent/task/$parent/children" ]; then
  pids="$pids $(cat "/proc/$parent/task/$parent/children")"
fi
if [ -r "/proc/$self/task/$self/children" ]; then
  pids="$pids $(cat "/proc/$self/task/$self/children")"
fi
scan_pid() {
  pid=$1
  n=$2
  case $pid in
    ''|*[!0-9]*) return ;;
  esac
  if [ -r "/proc/$pid/environ" ] && grep -a -F -q -- "$n" "/proc/$pid/environ"; then
    hits=$((hits + 1))
  fi
  if [ "$pid" = "$self" ] && env | grep -a -F -q -- "$n"; then
    hits=$((hits + 1))
  fi
  for f in /proc/$pid/fd/*; do
    [ -e "$f" ] || continue
    target=$(readlink "$f" 2>/dev/null || true)
    case $target in
      socket:*|'') continue ;;
    esac
    if timeout 0.2 dd if="$f" bs=4096 count=1 status=none iflag=nonblock 2>/dev/null | grep -a -F -q -- "$n"; then
      hits=$((hits + 1))
    fi
  done
}
for pid in $pids; do
  scan_pid "$pid" ` + shellQuote(needle) + `
  scan_pid "$pid" ` + shellQuote(key) + `
  scan_pid "$pid" ` + shellQuote(extra) + `
done
printf '%s\n' "$hits" > hits.txt`
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func fmtSscan(s string, n *int) (int, error) {
	s = strings.TrimSpace(s)
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, os.ErrInvalid
		}
		v = v*10 + int(c-'0')
	}
	*n = v
	return 1, nil
}
