package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/ingress"
	"github.com/polds/factory/internal/store"
)

const (
	plantedGitUser    = "planted-git-userinfo-77c1"
	plantedSnapName   = "planted-pod-name-88d2"
	plantedSnapText   = "planted-k8s-message-99e3"
	plantedHeaderHold = "planted-header-secret-aa14"
)

func TestM5bForgeTreeURL(t *testing.T) {
	const sha = "1f55a9fb2873481d8fcfa519cba9cafc3c8d1fa8"
	cases := []struct {
		name string
		raw  string
		sub  string
		want string
	}{
		{"https", "https://github.com/polds/factory.git", "", "https://github.com/polds/factory/tree/" + sha},
		{"https subdir", "https://github.com/polds/factory.git", "definitions/acme", "https://github.com/polds/factory/tree/" + sha + "/definitions/acme"},
		{"userinfo", "https://" + plantedGitUser + ":s3cret@github.com/polds/factory.git?token=1#frag", "", "https://github.com/polds/factory/tree/" + sha},
		{"scp", "git@github.com:polds/factory.git", "", "https://github.com/polds/factory/tree/" + sha},
		{"ssh", "ssh://git@github.com/polds/factory.git", "agents", "https://github.com/polds/factory/tree/" + sha + "/agents"},
		{"file", "file:///tmp/factory", "", ""},
		{"local", "/tmp/factory", "", ""},
		{"dotdot", "https://github.com/polds/factory.git", "../secrets", ""},
		{"slash", "https://github.com/polds/factory.git", `agents\secret`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := forgeTreeURL(tc.raw, sha, tc.sub)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if strings.Contains(got, plantedGitUser) || strings.Contains(got, "s3cret") || strings.Contains(got, "token=") {
				t.Fatalf("forge url kept secret material %s", got)
			}
		})
	}
	if forgeTreeURL("https://github.com/polds/factory.git", "", "") != "" {
		t.Fatal("empty sha produced a link")
	}
}

func TestM5bSnapshotWhitelist(t *testing.T) {
	raw := []byte(`{
		"kubernetes": {
			"observed_at": "2026-04-02T15:30:00Z",
			"truncated": true,
			"pod": {
				"name": "` + plantedSnapName + `",
				"uid": "uid-secret",
				"phase": "Failed",
				"reason": "Error",
				"conditions": [{"reason": "Ready", "message": "` + plantedSnapText + `"}]
			},
			"job": {"name": "` + plantedSnapName + `", "conditions": [{"reason": "Failed"}]},
			"container": {
				"name": "` + plantedSnapName + `",
				"state": "terminated",
				"waiting_reason": "",
				"termination_reason": "Error",
				"raw_exit_code": 0,
				"normalized_exit_code": 10,
				"signal": 0,
				"message": "` + plantedSnapText + `"
			},
			"events": [{"reason": "Killing", "message": "` + plantedSnapText + `"}]
		}
	}`)
	got := whitelistSnapshot(raw)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), plantedSnapName) || strings.Contains(string(encoded), plantedSnapText) || strings.Contains(string(encoded), "uid-secret") {
		t.Fatalf("snapshot leaked %s", encoded)
	}
	want := map[string]string{
		"pod.phase":                      "Failed",
		"pod.reason":                     "Error",
		"pod.conditions":                 "Ready",
		"job.conditions":                 "Failed",
		"container.state":                "terminated",
		"container.termination_reason":   "Error",
		"container.raw_exit_code":        "0",
		"container.normalized_exit_code": "10",
		"container.signal":               "0",
		"events":                         "Killing",
		"observed_at":                    "2026-04-02T15:30:00Z",
		"truncated":                      "true",
	}
	if len(got) != len(want) {
		t.Fatalf("snapshot %+v", got)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q want %q", key, got[key], value)
		}
	}
	if whitelistSnapshot([]byte(`{"other":1}`)) != nil {
		t.Fatal("missing kubernetes object was returned")
	}
}

func TestM5bMountedSwap(t *testing.T) {
	t.Run("copy fault keeps dest", func(t *testing.T) {
		src, dest := definitionPair(t)
		writeFile(t, filepath.Join(dest, "a.txt"), "old-a")
		writeFile(t, filepath.Join(dest, "b.txt"), "old-b")
		writeFile(t, filepath.Join(src, "a.txt"), "new-a")
		writeFile(t, filepath.Join(src, "b.txt"), "new-b")
		copies := 0
		setDefinitionOps(t, func(from, to string) error {
			copies++
			if copies == 2 {
				return errors.New("copy fault")
			}
			return copyFileSynced(from, to)
		}, func(old, newName string) error {
			if old == dest {
				return syscall.EBUSY
			}
			return os.Rename(old, newName)
		})
		err := installDefinitionDir(src, dest)
		if err == nil || errors.Is(err, errSwapIncomplete) {
			t.Fatalf("err %v", err)
		}
		if readFile(t, filepath.Join(dest, "a.txt")) != "old-a" || readFile(t, filepath.Join(dest, "b.txt")) != "old-b" {
			t.Fatal("dest changed")
		}
		if definitionTemps(t, filepath.Dir(dest)) != 0 {
			t.Fatal("temps or staging left behind")
		}
	})
	t.Run("copy fault removes new nested dirs", func(t *testing.T) {
		src, dest := definitionPair(t)
		writeFile(t, filepath.Join(dest, "keep", "old.txt"), "old")
		writeFile(t, filepath.Join(src, "agents", "a.txt"), "new-a")
		writeFile(t, filepath.Join(src, "agents", "b.txt"), "new-b")
		setDefinitionOps(t, func(from, to string) error {
			if filepath.Base(from) == "b.txt" {
				return errors.New("copy fault")
			}
			return copyFileSynced(from, to)
		}, func(old, newName string) error {
			if old == dest {
				return syscall.EBUSY
			}
			return os.Rename(old, newName)
		})
		err := installDefinitionDir(src, dest)
		if err == nil || errors.Is(err, errSwapIncomplete) {
			t.Fatalf("err %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dest, "agents")); !os.IsNotExist(statErr) {
			t.Fatalf("new agents dir left behind: %v", statErr)
		}
		if readFile(t, filepath.Join(dest, "keep", "old.txt")) != "old" {
			t.Fatal("existing tree changed")
		}
		if definitionTemps(t, filepath.Dir(dest)) != 0 {
			t.Fatal("temps or staging left behind")
		}
	})
	t.Run("rename fault keeps staging", func(t *testing.T) {
		src, dest := definitionPair(t)
		writeFile(t, filepath.Join(dest, "a.txt"), "old-a")
		writeFile(t, filepath.Join(dest, "b.txt"), "old-b")
		writeFile(t, filepath.Join(src, "a.txt"), "new-a")
		writeFile(t, filepath.Join(src, "b.txt"), "new-b")
		renames := 0
		setDefinitionOps(t, nil, func(old, newName string) error {
			if old == dest {
				return syscall.EBUSY
			}
			if strings.HasSuffix(filepath.Base(old), ".m5new") {
				renames++
				if renames == 2 {
					return errors.New("rename fault")
				}
			}
			return os.Rename(old, newName)
		})
		err := installDefinitionDir(src, dest)
		if !errors.Is(err, errSwapIncomplete) {
			t.Fatalf("err %v", err)
		}
		staging := findStaging(t, filepath.Dir(dest))
		if !strings.Contains(err.Error(), staging) {
			t.Fatalf("err %v does not name %s", err, staging)
		}
		setDefinitionOps(t, nil, func(old, newName string) error {
			if old == dest {
				return syscall.EBUSY
			}
			return os.Rename(old, newName)
		})
		if err := installDefinitionDir(src, dest); err != nil {
			t.Fatal(err)
		}
		if readFile(t, filepath.Join(dest, "a.txt")) != "new-a" || readFile(t, filepath.Join(dest, "b.txt")) != "new-b" {
			t.Fatal("retry did not install the new tree")
		}
		if definitionTemps(t, dest) != 0 {
			t.Fatal("temp files left in dest after retry")
		}
		if _, err := os.Stat(staging); err != nil {
			t.Fatal("failed staging directory was removed")
		}
	})
	t.Run("extras removed", func(t *testing.T) {
		src, dest := definitionPair(t)
		writeFile(t, filepath.Join(dest, "a.txt"), "old-a")
		writeFile(t, filepath.Join(dest, "c.txt"), "extra")
		writeFile(t, filepath.Join(dest, "old", "c.txt"), "extra-nested")
		if err := os.MkdirAll(filepath.Join(dest, "empty"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(src, "a.txt"), "new-a")
		writeFile(t, filepath.Join(src, "b.txt"), "new-b")
		setDefinitionOps(t, nil, func(old, newName string) error {
			if old == dest {
				return syscall.EBUSY
			}
			return os.Rename(old, newName)
		})
		if err := installDefinitionDir(src, dest); err != nil {
			t.Fatal(err)
		}
		if readFile(t, filepath.Join(dest, "a.txt")) != "new-a" || readFile(t, filepath.Join(dest, "b.txt")) != "new-b" {
			t.Fatal("new tree missing")
		}
		if _, err := os.Stat(filepath.Join(dest, "c.txt")); !os.IsNotExist(err) {
			t.Fatalf("extra file err %v", err)
		}
		if _, err := os.Stat(filepath.Join(dest, "old")); !os.IsNotExist(err) {
			t.Fatalf("empty extra dir err %v", err)
		}
		if _, err := os.Stat(filepath.Join(dest, "empty")); !os.IsNotExist(err) {
			t.Fatalf("empty dir err %v", err)
		}
		if definitionTemps(t, filepath.Dir(dest)) != 0 {
			t.Fatal("temps left behind")
		}
	})
}

func setDefinitionOps(t *testing.T, copyFn func(string, string) error, renameFn func(string, string) error) {
	t.Helper()
	definitionOpMu.Lock()
	prevCopy, prevRename := copyFile, renameFile
	if copyFn != nil {
		copyFile = copyFn
	}
	if renameFn != nil {
		renameFile = renameFn
	}
	definitionOpMu.Unlock()
	t.Cleanup(func() {
		definitionOpMu.Lock()
		copyFile, renameFile = prevCopy, prevRename
		definitionOpMu.Unlock()
	})
}

func definitionPair(t *testing.T) (src, dest string) {
	t.Helper()
	root := t.TempDir()
	src = filepath.Join(root, "src")
	dest = filepath.Join(root, "dest")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	return src, dest
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func findStaging(t *testing.T, parent string) string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var found string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".definition-") {
			if found != "" {
				t.Fatalf("two staging dirs in %s", parent)
			}
			found = filepath.Join(parent, entry.Name())
		}
	}
	if found == "" {
		t.Fatal("staging directory missing")
	}
	return found
}

func definitionTemps(t *testing.T, parent string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(parent, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, ".definition-") || strings.HasSuffix(name, ".m5new") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestM5bRunFieldsAndFactoryFilter(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "fields")
	asg := w.readAssignment(t)
	if asg.TaskID != runID {
		t.Fatalf("assignment %s", asg.TaskID)
	}
	ctx := context.Background()
	factoryID, _ := env.factoryAgent(t)
	workID, err := store.NewID(store.PrefixWorkItem)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `
		INSERT INTO work_items (id, factory_id, source, title, stage, status)
		VALUES ($1, $2, 'issue', 'fields', 'building', 'open')`, workID, factoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `UPDATE runs SET work_item_id = $2 WHERE id = $1`, runID, workID); err != nil {
		t.Fatal(err)
	}
	var created time.Time
	if err := env.raw.QueryRow(ctx, `SELECT created_at FROM runs WHERE id = $1`, runID).Scan(&created); err != nil {
		t.Fatal(err)
	}
	detail := env.runDetail(t, runID)
	if detail.Worker != "local-1" || detail.Source != "issue" || detail.StartedAt == nil {
		t.Fatalf("detail worker %q source %q started %v", detail.Worker, detail.Source, detail.StartedAt)
	}
	if !detail.CreatedAt.Truncate(time.Microsecond).Equal(created.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("created %s db %s", detail.CreatedAt, created)
	}
	row := findRunRow(t, env, runID)
	if row.Worker != "local-1" || row.Source != "issue" || row.StartedAt == nil {
		t.Fatalf("row %+v", row)
	}
	if !row.CreatedAt.Truncate(time.Microsecond).Equal(created.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("list created %s db %s", row.CreatedAt, created)
	}

	bareID, err := store.NewID(store.PrefixRun)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `
		INSERT INTO runs (id, factory_id, status, priority, pool)
		VALUES ($1, $2, 'queued', 1, 'standard')`, bareID, factoryID); err != nil {
		t.Fatal(err)
	}
	bare := env.runDetail(t, bareID)
	bareRaw := mustJSON(bare)
	if bare.Source != "" || bare.Worker != "" || strings.Contains(string(bareRaw), `"worker"`) || strings.Contains(string(bareRaw), `"source"`) {
		t.Fatalf("bare detail %s", bareRaw)
	}
	bareRow := findRunRow(t, env, bareID)
	rowRaw := mustJSON(bareRow)
	if bareRow.Source != "" || bareRow.Worker != "" || strings.Contains(string(rowRaw), `"worker"`) || strings.Contains(string(rowRaw), `"source"`) {
		t.Fatalf("bare row %s", rowRaw)
	}

	if _, err := env.raw.Exec(ctx, `
		INSERT INTO factories (id, name, schema_version, definition_source)
		VALUES ('fac_b', 'bee', 'v1alpha1', 'local')`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `
		INSERT INTO work_items (id, factory_id, source, stage, status)
		VALUES ('task_x', 'fac_b', 'issue', 'building', 'open')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 210; i++ {
		id := fmt.Sprintf("run_a_%03d", i)
		createdAt := time.Date(2026, 5, 1, 0, 0, i, 0, time.UTC)
		if _, err := env.raw.Exec(ctx, `
			INSERT INTO runs (id, factory_id, status, priority, pool, created_at)
			VALUES ($1, $2, 'queued', 1, 'standard', $3)`, id, factoryID, createdAt); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := env.raw.Exec(ctx, `
		INSERT INTO runs (id, factory_id, status, priority, pool, work_item_id, created_at)
		VALUES ('run_old', 'fac_b', 'queued', 1, 'standard', 'task_x', $1)`, old); err != nil {
		t.Fatal(err)
	}
	code, raw := env.authed(t, http.MethodGet, "/api/v1/agent/runs?factory_id=fac_b&work_item_id=task_x", "")
	if code != http.StatusOK {
		t.Fatalf("filter %d %s", code, raw)
	}
	var filtered []api.RunRow
	if err := json.Unmarshal(raw, &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ID != "run_old" || filtered[0].Source != "issue" {
		t.Fatalf("filtered %+v", filtered)
	}
	code, raw = env.authed(t, http.MethodGet, "/api/v1/agent/runs", "")
	if code != http.StatusOK {
		t.Fatalf("list %d %s", code, raw)
	}
	if strings.Contains(string(raw), "run_old") {
		t.Fatal("unfiltered page included the row past the limit")
	}
	code, raw = env.authed(t, http.MethodGet, "/api/v1/agent/runs?factory_id=missing-factory", "")
	if code != http.StatusNotFound {
		t.Fatalf("unknown factory %d %s", code, raw)
	}
}

func findRunRow(t *testing.T, env *env, id string) api.RunRow {
	t.Helper()
	code, raw := env.authed(t, http.MethodGet, "/api/v1/agent/runs?factory_id=m2", "")
	if code != http.StatusOK {
		t.Fatalf("list %d %s", code, raw)
	}
	var rows []api.RunRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("run %s missing in %s", id, raw)
	return api.RunRow{}
}

func assertListMatchesDetail(t *testing.T, env *env, runID string) {
	t.Helper()
	detail := env.runDetail(t, runID)
	row := findRunRow(t, env, runID)
	if row.FailureCode != detail.Failure.Code {
		t.Fatalf("list code %q detail %q", row.FailureCode, detail.Failure.Code)
	}
	if (row.ExitCode == nil) != (detail.ExitCode == nil) || (row.ExitCode != nil && *row.ExitCode != *detail.ExitCode) {
		t.Fatalf("list exit %v detail %v", row.ExitCode, detail.ExitCode)
	}
}

func TestM5bSteerHistory(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "history")
	asg := w.readAssignment(t)
	env.waitStatus(t, runID, "running")
	token := asg.EnvVars["FACTORY_RUN_TOKEN"]
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], token, 0, []string{
		`{"type":"run.capabilities","steer":true}`,
	})
	ctx := context.Background()
	first, err := env.srv.Interrupt(ctx, runID, api.APIKeyPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	second, err := env.srv.Followup(ctx, runID, api.APIKeyPrincipal, "try the other file")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	var claimed []api.Steer
	var claimErr error
	go func() {
		defer wg.Done()
		claimed, claimErr = env.srv.NextSteers(ctx, token, runID, "", 0)
	}()
	go func() {
		defer wg.Done()
		for range 30 {
			if _, err := env.srv.ListSteers(ctx, runID); err != nil {
				t.Errorf("list %v", err)
				return
			}
		}
	}()
	wg.Wait()
	if claimErr != nil || len(claimed) != 2 {
		t.Fatalf("claimed %+v %v", claimed, claimErr)
	}
	again, err := env.srv.NextSteers(ctx, token, runID, "", 0)
	if err != nil || len(again) != 0 {
		t.Fatalf("second claim %+v %v", again, err)
	}
	var delivered *time.Time
	if err := env.raw.QueryRow(ctx, `SELECT delivered_at FROM steer_requests WHERE id = $1`, first).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered != nil {
		t.Fatal("list set delivered_at")
	}
	acked, err := env.srv.NextSteers(ctx, token, runID, first, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = acked
	rows, err := env.srv.ListSteers(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != first || rows[1].ID != second {
		t.Fatalf("order %+v", rows)
	}
	if rows[0].DeliveredAt == nil || rows[1].DeliveredAt != nil {
		t.Fatalf("delivered %+v %+v", rows[0].DeliveredAt, rows[1].DeliveredAt)
	}
	if rows[0].Text != "" || rows[1].Text != "try the other file" || rows[0].Actor == "" || rows[1].Kind != "followup" {
		t.Fatalf("rows %+v", rows)
	}
	code, raw := env.authed(t, http.MethodGet, "/api/v1/agent/runs/"+runID+"/steers", "")
	if code != http.StatusOK {
		t.Fatalf("steers %d %s", code, raw)
	}
	if _, err := env.srv.ListSteers(ctx, "missing"); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("missing %v", err)
	}
	code, raw = env.authed(t, http.MethodGet, "/api/v1/agent/runs/missing/steers", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing steers %d %s", code, raw)
	}
}

func TestM5bNormalizedEvents(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{
		definition: func(t *testing.T) string {
			t.Helper()
			return writeM4Definition(t)
		},
		configure: func(cfg *Config) {
			cfg.GitHubWebhookSecret = e2eWebhook
		},
	})
	postGitHub(t, env.base+"/api/v1/ingress/github", e2eWebhook, "issues", "delivery-m5b", labeledIssueNumber(77))
	deadline := time.Now().Add(15 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		code, raw := env.authed(t, http.MethodGet, "/api/v1/factory/m4/deliveries?include=body", "")
		if code == http.StatusOK && strings.Contains(string(raw), "normalized_events") {
			body = raw
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if body == nil {
		t.Fatal("normalized events did not appear")
	}
	if strings.Contains(string(body), e2eWebhook) || strings.Contains(string(body), `"raw"`) {
		t.Fatalf("delivery leaked %s", body)
	}
	var rows []api.Delivery
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].NormalizedEvents) != 1 {
		t.Fatalf("rows %+v", rows)
	}
	var ev map[string]any
	if err := json.Unmarshal(rows[0].NormalizedEvents[0], &ev); err != nil {
		t.Fatal(err)
	}
	if ev["provider"] == "" || ev["event"] == "" || ev["subject"] == nil || ev["actor"] == nil {
		t.Fatalf("event %+v", ev)
	}
	if _, ok := ev["raw"]; ok {
		t.Fatal("raw key present")
	}
	code, raw := env.authed(t, http.MethodPost, "/api/v1/deliveries/"+rows[0].ID+":replay", "")
	if code != http.StatusOK {
		t.Fatalf("replay %d %s", code, raw)
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		code, raw = env.authed(t, http.MethodGet, "/api/v1/factory/m4/deliveries", "")
		if code == http.StatusOK {
			var again []api.Delivery
			if err := json.Unmarshal(raw, &again); err != nil {
				t.Fatal(err)
			}
			if len(again) == 1 && len(again[0].NormalizedEvents) == 1 {
				body = raw
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	var replayed []api.Delivery
	if err := json.Unmarshal(body, &replayed); err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 || len(replayed[0].NormalizedEvents) != 1 {
		t.Fatalf("replayed %+v", replayed)
	}

	ctx := context.Background()
	factoryID, _ := env.factoryAgent(t)
	reg, err := env.srv.store.UpsertWebhookRegistration(ctx, store.WebhookRegistration{
		FactoryID: factoryID, Name: "ci", AuthMode: store.WebhookAuthToken, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := env.srv.store.InsertDelivery(ctx, store.Delivery{
		FactoryID:  factoryID,
		Provider:   store.ProviderWebhook,
		WebhookUID: reg.UID,
		DeliveryID: "ci-1",
		Headers:    map[string]string{"X-Webhook-Secret": plantedHeaderHold},
		Body:       []byte(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = env.srv.handleDelivery(ctx, ingress.Delivery{
		ID: stored.ID, Provider: stored.Provider, WebhookUID: stored.WebhookUID,
		DeliveryID: stored.DeliveryID, Body: []byte(`{"ok":true}`), Headers: stored.Headers,
	})
	code, raw = env.authed(t, http.MethodGet, "/api/v1/factory/m4/webhooks/"+reg.UID+"/deliveries?include=body", "")
	if code != http.StatusOK {
		t.Fatalf("webhook deliveries %d %s", code, raw)
	}
	if strings.Contains(string(raw), plantedHeaderHold) || strings.Contains(string(raw), `"raw"`) {
		t.Fatalf("webhook delivery leaked %s", raw)
	}
	var hooks []api.Delivery
	if err := json.Unmarshal(raw, &hooks); err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || len(hooks[0].NormalizedEvents) != 1 || hooks[0].ID != stored.ID {
		t.Fatalf("hooks %+v", hooks)
	}
	code, raw = env.authed(t, http.MethodGet, "/api/v1/factory/m4/webhooks/missing/deliveries", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing webhook %d %s", code, raw)
	}

	nullRow, _, err := env.srv.store.InsertDelivery(ctx, store.Delivery{
		FactoryID: factoryID, Provider: store.ProviderWebhook, WebhookUID: reg.UID,
		DeliveryID: "ci-null", Body: []byte(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	emptyRow, _, err := env.srv.store.InsertDelivery(ctx, store.Delivery{
		FactoryID: factoryID, Provider: store.ProviderWebhook, WebhookUID: reg.UID,
		DeliveryID: "ci-empty", Body: []byte(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.srv.store.SetDeliveryNormalized(ctx, emptyRow.ID, nil); err != nil {
		t.Fatal(err)
	}
	for _, include := range []string{"", "?include=body"} {
		code, raw = env.authed(t, http.MethodGet, "/api/v1/factory/m4/webhooks/"+reg.UID+"/deliveries"+include, "")
		if code != http.StatusOK {
			t.Fatalf("shapes %s %d %s", include, code, raw)
		}
		var raws []json.RawMessage
		if err := json.Unmarshal(raw, &raws); err != nil {
			t.Fatal(err)
		}
		byID := map[string]json.RawMessage{}
		for _, item := range raws {
			var id struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(item, &id); err != nil {
				t.Fatal(err)
			}
			byID[id.ID] = item
		}
		if strings.Contains(string(byID[nullRow.ID]), "normalized_events") {
			t.Fatalf("null column kept the key (%s): %s", include, byID[nullRow.ID])
		}
		if !bytes.Contains(byID[emptyRow.ID], []byte(`"normalized_events":[]`)) {
			t.Fatalf("empty list omitted (%s): %s", include, byID[emptyRow.ID])
		}
		if !bytes.Contains(byID[stored.ID], []byte(`"normalized_events":[`)) || bytes.Contains(byID[stored.ID], []byte(`"raw"`)) {
			t.Fatalf("events (%s): %s", include, byID[stored.ID])
		}
		for _, id := range []string{nullRow.ID, emptyRow.ID, stored.ID} {
			hasBody := bytes.Contains(byID[id], []byte(`"body":`))
			if hasBody != (include != "") {
				t.Fatalf("body %s include %q: %s", id, include, byID[id])
			}
		}
	}
}

func TestM5bWebhookDeliveryPage(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	ctx := context.Background()
	factoryID, _ := env.factoryAgent(t)
	reg, err := env.srv.store.UpsertWebhookRegistration(ctx, store.WebhookRegistration{
		FactoryID: factoryID, Name: "page", AuthMode: store.WebhookAuthToken, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		_, _, err := env.srv.store.InsertDelivery(ctx, store.Delivery{
			FactoryID:  factoryID,
			Provider:   store.ProviderWebhook,
			WebhookUID: reg.UID,
			DeliveryID: fmt.Sprintf("d-%02d", i),
			ReceivedAt: base.Add(time.Duration(i) * time.Second),
			Body:       []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	code, raw := env.authed(t, http.MethodGet, "/api/v1/factory/"+factoryID+"/webhooks/"+reg.UID+"/deliveries", "")
	if code != http.StatusOK {
		t.Fatalf("page %d %s", code, raw)
	}
	var rows []api.Delivery
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 50 {
		t.Fatalf("page len %d", len(rows))
	}
	if rows[0].DeliveryID != "d-59" || rows[49].DeliveryID != "d-10" {
		t.Fatalf("order %s .. %s", rows[0].DeliveryID, rows[49].DeliveryID)
	}
	if _, err := env.raw.Exec(ctx, `
		INSERT INTO factories (id, name, schema_version, definition_source)
		VALUES ('fac_other', 'other', 'v1alpha1', 'local')`); err != nil {
		t.Fatal(err)
	}
	other, err := env.srv.store.UpsertWebhookRegistration(ctx, store.WebhookRegistration{
		FactoryID: "fac_other", Name: "other", AuthMode: store.WebhookAuthToken, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, raw = env.authed(t, http.MethodGet, "/api/v1/factory/"+factoryID+"/webhooks/"+other.UID+"/deliveries", "")
	if code != http.StatusNotFound {
		t.Fatalf("other factory webhook %d %s", code, raw)
	}
}

func TestM5bIdleAutomationMinutes(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{maxConcurrent: 1, definition: func(t *testing.T) string {
		t.Helper()
		root := writeDefinition(t)
		mustWrite(t, root+"/agents/implementer/agent.md", `
---
description: Implements the requested change.
agentType: IMPLEMENT
model: test-model
secrets:
  - NPM_TOKEN
maxTurns: 7
idleTimeoutMinutes: 5
---

Do the task.
`)
		mustWrite(t, root+"/automations/nightly/automation.md", `
---
enabled: true
agent: implementer
idleTimeoutMinutes: 7
---

Nightly.
`)
		return root
	}})
	runID := env.startRun(t, "auto idle")
	ctx := context.Background()
	workID, err := store.NewID(store.PrefixWorkItem)
	if err != nil {
		t.Fatal(err)
	}
	factoryID, _ := env.factoryAgent(t)
	if _, err := env.raw.Exec(ctx, `
		INSERT INTO work_items (id, factory_id, source, title, stage, status)
		VALUES ($1, $2, 'manual', 'nightly', 'triage', 'open')`, workID, factoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `
		INSERT INTO intake_slots (work_item_id, automation, delivery_id, run_id)
		VALUES ($1, 'nightly', 'slot-m5b', $2)`, workID, runID); err != nil {
		t.Fatal(err)
	}
	w := dialWorker(t, env, "local-1")
	defer w.close()
	asg := w.readAssignment(t)
	if asg.TaskID != runID || asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || asg.Task.AgentConfigSnapshot.IdleTimeoutMinutes == nil || *asg.Task.AgentConfigSnapshot.IdleTimeoutMinutes != 7 {
		t.Fatalf("automation assignment %+v", asg.Task)
	}
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"terminal","status":"succeeded","summary":"done","ts":"2026-04-02T12:00:00Z"}`,
	})
	waitIdle(t, env, runID, time.Date(2026, 4, 2, 12, 7, 0, 0, time.UTC))

	other := env.startRun(t, "agent idle")
	w2 := dialWorker(t, env, "local-2")
	defer w2.close()
	asg2 := w2.readAssignment(t)
	if asg2.TaskID != other || asg2.Task == nil || asg2.Task.AgentConfigSnapshot == nil || asg2.Task.AgentConfigSnapshot.IdleTimeoutMinutes == nil || *asg2.Task.AgentConfigSnapshot.IdleTimeoutMinutes != 5 {
		t.Fatalf("agent assignment %+v", asg2.Task)
	}
	postEvents(t, asg2.EnvVars["FACTORY_EVENTS_URL"], asg2.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"terminal","status":"succeeded","summary":"done","ts":"2026-04-02T12:00:00Z"}`,
	})
	waitIdle(t, env, other, time.Date(2026, 4, 2, 12, 5, 0, 0, time.UTC))
}

func waitIdle(t *testing.T, env *env, runID string, want time.Time) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var detail api.RunDetail
	for time.Now().Before(deadline) {
		detail = env.runDetail(t, runID)
		if detail.Steer.IdleUntil != nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if detail.Steer.IdleUntil == nil || !detail.Steer.IdleUntil.Equal(want) {
		t.Fatalf("idle_until %v want %s", detail.Steer.IdleUntil, want)
	}
}

func plantNoSecretSurfaces(t *testing.T, env *env) (runID, webhookUID string, extra []string) {
	t.Helper()
	extra = []string{plantedGitUser, plantedSnapName, plantedSnapText, plantedHeaderHold}
	ctx := context.Background()
	if err := env.raw.QueryRow(ctx, `SELECT id FROM runs ORDER BY created_at DESC LIMIT 1`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	env.srv.mu.Lock()
	env.srv.gitSHA = "1f55a9fb2873481d8fcfa519cba9cafc3c8d1fa8"
	env.srv.cfg.DefinitionGitURL = "https://" + plantedGitUser + ":s3cret@github.com/acme/factory.git"
	env.srv.mu.Unlock()
	snap := `{
		"kubernetes": {
			"pod": {"name": "` + plantedSnapName + `", "phase": "Failed", "reason": "Error"},
			"container": {"message": "` + plantedSnapText + `", "state": "terminated", "normalized_exit_code": 1},
			"events": [{"reason": "Killing", "message": "` + plantedSnapText + `"}]
		}
	}`
	if _, err := env.raw.Exec(ctx, `
		UPDATE executions SET failure_details_json = $2::jsonb WHERE run_id = $1`, runID, snap); err != nil {
		t.Fatal(err)
	}
	factoryID, _ := env.factoryAgent(t)
	reg, err := env.srv.store.UpsertWebhookRegistration(ctx, store.WebhookRegistration{
		FactoryID: factoryID, Name: "planted", AuthMode: store.WebhookAuthToken, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := env.srv.store.InsertDelivery(ctx, store.Delivery{
		FactoryID: factoryID, Provider: store.ProviderWebhook, WebhookUID: reg.UID,
		DeliveryID: "planted-1",
		Headers:    map[string]string{"X-Webhook-Secret": plantedHeaderHold, "X-Hub-Signature-256": plantedHeaderHold},
		Body:       []byte(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = env.srv.handleDelivery(ctx, ingress.Delivery{
		ID: stored.ID, Provider: stored.Provider, WebhookUID: stored.WebhookUID,
		DeliveryID: stored.DeliveryID, Body: []byte(`{"ok":true}`),
	})
	return runID, reg.UID, extra
}
