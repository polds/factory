package server

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/polds/factory/internal/definition"
	ghclient "github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/store"
)

func TestFactorySecretValuesDoNotCross(t *testing.T) {
	s := &Server{cfg: Config{
		Secrets: map[string]string{"TOKEN": "unprefixed"},
		FactorySecrets: map[string]map[string]string{
			"fac_a": {"TOKEN": "value-a"},
			"fac_b": {"TOKEN": "value-b"},
		},
	}}
	got, err := s.resolveFactorySecret(context.Background(), "fac_b", "TOKEN")
	if err != nil || got != "value-b" || strings.Contains(got, "value-a") || got == "unprefixed" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := s.resolveFactorySecret(context.Background(), "fac_b", "OTHER"); err == nil {
		t.Fatal("missing name resolved")
	}
	if _, err := s.resolveFactorySecret(context.Background(), "fac_c", "TOKEN"); err == nil {
		t.Fatal("unprefixed name resolved for a factory")
	}
}

func TestBootstrapSecretBindsAfterCreate(t *testing.T) {
	s := &Server{cfg: Config{BootstrapSecrets: map[string]string{"WIDGET_TOKEN": "boot-value"}}}
	if _, err := s.resolveFactorySecret(context.Background(), "fac_boot", "WIDGET_TOKEN"); err == nil {
		t.Fatal("bootstrap secret resolved before create")
	}
	s.bindBootstrapSecrets("fac_boot")
	got, err := s.resolveFactorySecret(context.Background(), "fac_boot", "WIDGET_TOKEN")
	if err != nil || got != "boot-value" {
		t.Fatalf("after create %q %v", got, err)
	}
	if _, err := s.resolveFactorySecret(context.Background(), "fac_other", "WIDGET_TOKEN"); err == nil {
		t.Fatal("other factory received the bootstrap value")
	}
}

func TestPublishPlanRefusesRepoOutsideFactory(t *testing.T) {
	plan := publishPlan{
		enabled: true, repo: "other/repo", issueKey: "widget#1",
		refPrefix: defaultPublishPrefix, title: "draft", body: defaultPublishBody,
		listed: map[string]struct{}{"example/widget": {}},
	}
	err := plan.closed()
	if err == nil || !strings.Contains(err.Error(), reasonRepo) {
		t.Fatal(err)
	}
	for _, needle := range []string{"99", "https://", "factory/draft-abc", strings.Repeat("a", 40)} {
		if strings.Contains(err.Error(), needle) {
			t.Fatalf("error leaked %s: %v", needle, err)
		}
	}
	plan.repo = "example/widget"
	if err := plan.closed(); err != nil {
		t.Fatal(err)
	}
	plan.enabled = false
	if err := plan.closed(); err != nil {
		t.Fatal(err)
	}
}

func TestMintRefusesRepoBeforeInstallationLookup(t *testing.T) {
	app := &countApp{}
	s := &Server{
		cfg:   Config{GitHubTokenMode: tokenModeApp},
		integ: m4State{githubApp: app},
		loaded: loaded{
			factory: store.Factory{ID: "fac_b"},
			tree: &definition.Tree{Factory: definition.Factory{Repositories: []definition.Repository{
				{Owner: "example", Name: "widget"},
			}}},
		},
	}
	_, err := s.mintGitHubToken(context.Background(), store.Run{FactoryID: "fac_b", Repo: "other/repo"}, boundAgent{})
	if err == nil || app.lookups != 0 {
		t.Fatalf("err %v lookups %d", err, app.lookups)
	}
}

func TestRoleFilesMintIsReadOnly(t *testing.T) {
	app := &countApp{install: 7}
	s := &Server{cfg: Config{GitHubTokenMode: tokenModeApp}, integ: m4State{githubApp: app}}
	s.roleFilesForTest = true
	_, err := s.mintGitHubToken(context.Background(), store.Run{Repo: "example/widget"}, boundAgent{})
	if err == nil || app.lookups != 1 {
		t.Fatalf("err %v lookups %d", err, app.lookups)
	}
	if app.perms["contents"] != "read" || app.perms["metadata"] != "read" || app.perms["pull_requests"] != "" {
		t.Fatalf("perms %+v", app.perms)
	}
}

func TestStoreNameOmitsRetiredPrefix(t *testing.T) {
	body, err := os.ReadFile("disposable.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	start := strings.Index(text, "func storeName")
	end := strings.Index(text[start:], "\nfunc ")
	chunk := text[start : start+end]
	create := strings.Index(text, "func newDisposableName")
	createEnd := strings.Index(text[create:], "\nfunc ")
	chunk += text[create : create+createEnd]
	if strings.Contains(strings.ToLower(chunk), string([]byte{100, 114, 101, 97, 109, 100})) {
		t.Fatal("create path names the retired prefix")
	}
	name, err := newDisposableName()
	if err != nil || !storeName(name) || !strings.HasPrefix(name, "factory_ev_") {
		t.Fatalf("name %s %v", name, err)
	}
}

type countApp struct {
	lookups int
	perms   map[string]string
	install int64
}

func (c *countApp) InstallationByRepo(context.Context, string) (int64, error) {
	c.lookups++
	if c.install == 0 {
		return 0, errors.New("installation lookup")
	}
	return c.install, nil
}

func (c *countApp) InstallationToken(_ context.Context, _ int64, _ []string, perms map[string]string) (ghclient.Token, error) {
	c.perms = perms
	return ghclient.Token{}, nil
}

func (c *countApp) CreateCheckRun(context.Context, ghclient.Token, string, string, ghclient.CheckRun) (int64, error) {
	return 0, nil
}

func (c *countApp) UpdateCheckRun(context.Context, ghclient.Token, string, int64, ghclient.CheckRun) error {
	return nil
}

func (c *countApp) UpsertIssueComment(context.Context, ghclient.Token, string, int, string, string) error {
	return nil
}
