package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/client"
	"github.com/polds/factory/internal/defsync"
)

func TestDefinitionPush(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	t.Run("git backed writes nothing", func(t *testing.T) {
		dir := writeDefinition(t)
		before := treeDigest(t, dir)
		env := boot(t, bootOpts{
			definition:  func(*testing.T) string { return dir },
			gitURL:      "https://example.invalid/factory.git",
			gitRef:      "main",
			gitInterval: time.Hour,
			gitCloner:   errClone{},
		})
		c := env.factoryClient()
		_, err := c.PushDefinition(context.Background(), "m2", api.DefinitionPushRequest{
			Generation: 1,
			Files:      []api.DefinitionFile{{Path: "factory.yaml", Content: "name: other\n"}},
		})
		if !errors.Is(err, client.ErrGitBackedFactory) {
			t.Fatalf("err %v", err)
		}
		if got := treeDigest(t, dir); got != before {
			t.Fatalf("git-backed push changed the tree %s -> %s", before, got)
		}
	})

	t.Run("path backed writes nothing", func(t *testing.T) {
		dir := writeDefinition(t)
		before := treeDigest(t, dir)
		env := boot(t, bootOpts{definition: func(*testing.T) string { return dir }})
		c := env.factoryClient()
		_, err := c.PushDefinition(context.Background(), "m2", api.DefinitionPushRequest{
			Generation: 1,
			Files:      m2Files("one"),
		})
		if !errors.Is(err, client.ErrGitBackedFactory) {
			t.Fatalf("err %v", err)
		}
		if got := treeDigest(t, dir); got != before {
			t.Fatal("path-backed push wrote")
		}
	})

	t.Run("publish", func(t *testing.T) {
		env := boot(t, bootOpts{
			definition: func(t *testing.T) string { return t.TempDir() },
			configure:  func(cfg *Config) { cfg.DefinitionSource = "api" },
		})
		c := env.factoryClient()
		ctx := context.Background()
		first := m2Files("one")
		got, err := c.PushDefinition(ctx, "m2", api.DefinitionPushRequest{Generation: 1, Files: first})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Valid || got.PublishedGeneration != 1 || len(got.Errors) != 0 {
			t.Fatalf("first %+v", got)
		}
		view, err := c.GetDefinition(ctx, "m2")
		if err != nil {
			t.Fatal(err)
		}
		if sig := fileSig(view.Files); sig != fileSig(servedOf(first)) {
			t.Fatalf("served %s want %s", sig, fileSig(servedOf(first)))
		}

		stale, err := c.PushDefinition(ctx, "m2", api.DefinitionPushRequest{Generation: 1, Files: m2Files("stale")})
		if err != nil {
			t.Fatal(err)
		}
		if !stale.Valid || stale.PublishedGeneration != 1 {
			t.Fatalf("stale %+v", stale)
		}
		view, err = c.GetDefinition(ctx, "m2")
		if err != nil {
			t.Fatal(err)
		}
		if sig := fileSig(view.Files); sig != fileSig(servedOf(first)) {
			t.Fatalf("stale write changed the tree %s", sig)
		}

		second := m2Files("two")
		second = append(second, api.DefinitionFile{Path: "runners/batch.yaml", Content: runnerYAML})
		hold := make(chan struct{})
		entered := make(chan struct{})
		env.srv.beforeAPISwap = func() {
			close(entered)
			<-hold
		}
		var pushed api.DefinitionPushResponse
		var pushErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			pushed, pushErr = c.PushDefinition(ctx, "m2", api.DefinitionPushRequest{Generation: 2, Files: second})
		}()
		select {
		case <-entered:
		case <-done:
			t.Fatalf("push finished before the swap hook: %v", pushErr)
		case <-time.After(20 * time.Second):
			t.Fatal("timed out waiting for the swap hook")
		}
		var bad error
		var mu sync.Mutex
		var wg sync.WaitGroup
		stop := make(chan struct{})
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					view, err := c.GetDefinition(ctx, "m2")
					if err != nil {
						mu.Lock()
						if bad == nil {
							bad = err
						}
						mu.Unlock()
						return
					}
					sig := fileSig(view.Files)
					if sig != fileSig(servedOf(first)) && sig != fileSig(servedOf(second)) {
						mu.Lock()
						bad = errors.New("partial tree " + sig)
						mu.Unlock()
						return
					}
				}
			}()
		}
		time.Sleep(50 * time.Millisecond)
		close(hold)
		<-done
		close(stop)
		wg.Wait()
		if bad != nil {
			t.Fatal(bad)
		}
		if pushErr != nil {
			t.Fatal(pushErr)
		}
		if !pushed.Valid || pushed.PublishedGeneration != 2 {
			t.Fatalf("second %+v", pushed)
		}
		view, err = c.GetDefinition(ctx, "m2")
		if err != nil {
			t.Fatal(err)
		}
		if fileSig(view.Files) != fileSig(servedOf(second)) {
			t.Fatalf("after swap %s", fileSig(view.Files))
		}

		const planted = "sk-plantedFactorySecretValue99"
		badFiles := m2Files("two")
		badFiles[0].Content += "\n" + planted + ": true\n"
		invalid, err := c.PushDefinition(ctx, "m2", api.DefinitionPushRequest{Generation: 3, Files: badFiles})
		if err != nil {
			t.Fatal(err)
		}
		if invalid.Valid || invalid.PublishedGeneration != 2 {
			t.Fatalf("invalid %+v", invalid)
		}
		blob, err := jsonMarshal(invalid)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(blob, []byte(planted)) {
			t.Fatalf("diagnostic leaked: %s", blob)
		}
		view, err = c.GetDefinition(ctx, "m2")
		if err != nil {
			t.Fatal(err)
		}
		if fileSig(view.Files) != fileSig(servedOf(second)) {
			t.Fatal("invalid push replaced the served tree")
		}
		if bytes.Contains([]byte(fileSig(view.Files)), []byte(planted)) {
			t.Fatal("served tree contains the planted secret")
		}
	})
}

func (e *env) factoryClient() *client.Client {
	return &client.Client{BaseURL: e.base, APIKey: e.apiKey}
}

func m2Files(mark string) []api.DefinitionFile {
	return []api.DefinitionFile{
		{Path: "factory.yaml", Content: "schemaVersion: v1alpha1\nname: m2\ndescription: " + mark + "\nsecrets:\n  - GH_TOKEN\nagentDefaults:\n  model: auto\n  runner: local\n"},
		{Path: "runners/local.yaml", Content: runnerYAML},
		{Path: "agents/implementer/agent.md", Content: "---\ndescription: Implements the requested change.\nagentType: IMPLEMENT\nmodel: test-model\nsecrets:\n  - NPM_TOKEN\nmaxTurns: 7\n---\n\nDo the task.\n"},
	}
}

const runnerYAML = "description: Local docker runner\nplatform:\n  os: linux\n  arch: x86_64\n  linux:\n    dockerImage: factory-m1-task:dev\n"

func servedOf(files []api.DefinitionFile) []api.DefinitionFile {
	out := append([]api.DefinitionFile(nil), files...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func fileSig(files []api.DefinitionFile) string {
	cp := servedOf(files)
	h := sha256.New()
	for _, file := range cp {
		_, _ = io.WriteString(h, file.Path)
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, file.Content)
		_, _ = io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func treeDigest(t *testing.T, dir string) string {
	t.Helper()
	files, err := definitionFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	return fileSig(files)
}

func jsonMarshal(v api.DefinitionPushResponse) ([]byte, error) {
	return json.Marshal(v)
}

type errClone struct{}

func (errClone) Clone(context.Context, string, string, string) (string, error) {
	return "", errors.New("clone disabled")
}

var _ defsync.Cloner = errClone{}
