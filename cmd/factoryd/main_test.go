package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polds/factory/internal/server"
)

func TestNoArgsServes(t *testing.T) {
	var stderr strings.Builder
	code := execute(nil, func(string) string { return "" }, nil, io.Discard, &stderr)
	if code != 1 {
		t.Fatalf("exit %d %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "FACTORY_DATABASE_URL") {
		t.Fatal(stderr.String())
	}
}

func TestUsageMentionsTaskServerURL(t *testing.T) {
	var stderr strings.Builder
	if execute([]string{"nope"}, func(string) string { return "" }, nil, io.Discard, &stderr) != 2 {
		t.Fatalf("exit %s", stderr.String())
	}
	text := stderr.String()
	if !strings.Contains(text, "FACTORY_TASK_SERVER_URL") || !strings.Contains(text, "172.17.0.1") {
		t.Fatal(text)
	}
	stderr.Reset()
	if execute([]string{"migrate", "down"}, func(string) string { return "" }, nil, io.Discard, &stderr) != 2 {
		t.Fatal(stderr.String())
	}
}

func TestTaskServerURLDefault(t *testing.T) {
	cfg, err := configFromEnv(func(k string) string {
		switch k {
		case "FACTORY_DATABASE_URL":
			return "postgres://example"
		case "FACTORY_API_KEY":
			return "api-key-value"
		case "FACTORY_WORKER_API_KEY":
			return "worker-key-value"
		case "FACTORY_DEFINITION_PATH":
			return "/definitions"
		default:
			return ""
		}
	}, []string{"FACTORY_SECRET_GH_TOKEN=planted-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.server.TaskServerURL != server.DefaultTaskServerURL {
		t.Fatal(cfg.server.TaskServerURL)
	}
	if cfg.server.Secrets["GH_TOKEN"] != "planted-secret-value" {
		t.Fatal("secret name was not taken from FACTORY_SECRET_")
	}
	if !cfg.server.S3.PathStyle || cfg.server.S3.Region != "us-east-1" || cfg.addr != ":8080" {
		t.Fatalf("s3 path %v region %s addr %s", cfg.server.S3.PathStyle, cfg.server.S3.Region, cfg.addr)
	}
}

func TestConfigReadsDefinitionGit(t *testing.T) {
	cfg, err := configFromEnv(func(k string) string {
		switch k {
		case "FACTORY_DATABASE_URL":
			return "postgres://example"
		case "FACTORY_API_KEY":
			return "api-key-value"
		case "FACTORY_WORKER_API_KEY":
			return "worker-key-value"
		case "FACTORY_DEFINITION_PATH":
			return "/definitions"
		case "FACTORY_SCHEMAS_PATH":
			return "/schemas"
		case "FACTORY_DEFINITION_GIT_URL":
			return "https://example.invalid/factory.git"
		case "FACTORY_DEFINITION_GIT_REF":
			return "release"
		case "FACTORY_DEFINITION_GIT_SUBDIR":
			return "deploy/m3-factory"
		case "FACTORY_DEFINITION_GIT_PRODUCTION_REF":
			return "release"
		case "FACTORY_DEFINITION_GIT_INTERVAL":
			return "45s"
		default:
			return ""
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.server.GitHubTokenMode != "pat" {
		t.Fatalf("token mode %q", cfg.server.GitHubTokenMode)
	}
	if cfg.server.SchemasPath != "/schemas" || cfg.server.DefinitionGitURL != "https://example.invalid/factory.git" {
		t.Fatalf("schema %q git %q", cfg.server.SchemasPath, cfg.server.DefinitionGitURL)
	}
	if cfg.server.DefinitionGitRef != "release" || cfg.server.DefinitionGitSubdir != "deploy/m3-factory" {
		t.Fatalf("ref %q subdir %q", cfg.server.DefinitionGitRef, cfg.server.DefinitionGitSubdir)
	}
	if cfg.server.DefinitionGitProductionRef != "release" || cfg.server.DefinitionGitInterval.String() != "45s" {
		t.Fatalf("production %q interval %s", cfg.server.DefinitionGitProductionRef, cfg.server.DefinitionGitInterval)
	}
}

func TestHealthcheck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	code := execute([]string{"healthcheck"}, func(k string) string {
		if k == "FACTORY_HEALTHCHECK_URL" {
			return ts.URL + "/healthz"
		}
		return ""
	}, nil, io.Discard, io.Discard)
	if code != 0 {
		t.Fatal(code)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusServiceUnavailable)
	}))
	t.Cleanup(bad.Close)
	var stderr strings.Builder
	code = execute([]string{"healthcheck"}, func(k string) string {
		if k == "FACTORY_HEALTHCHECK_URL" {
			return bad.URL
		}
		return ""
	}, nil, io.Discard, &stderr)
	if code != 1 {
		t.Fatalf("exit %d %s", code, stderr.String())
	}
}

func TestMigrateUp(t *testing.T) {
	url := os.Getenv("FACTORY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	var stderr strings.Builder
	code := execute([]string{"migrate", "up"}, func(k string) string {
		if k == "FACTORY_DATABASE_URL" {
			return url
		}
		return ""
	}, nil, io.Discard, &stderr)
	if code != 0 {
		t.Fatal(stderr.String())
	}
}

func TestConfigReadsM4(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	path := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	const webhook = "whsec-config-value"
	cfg, err := configFromEnv(func(k string) string {
		switch k {
		case "FACTORY_GITHUB_APP_ID":
			return "4242"
		case "FACTORY_GITHUB_APP_PRIVATE_KEY_FILE":
			return path
		case "FACTORY_GITHUB_WEBHOOK_SECRET":
			return webhook
		case "FACTORY_GITHUB_TOKEN_MODE":
			return "app"
		case "FACTORY_SLACK_BOT_TOKEN":
			return "xoxb-config"
		case "FACTORY_SLACK_SIGNING_SECRET":
			return "slack-sign"
		case "FACTORY_SLACK_TEAM_ID":
			return "T0FACTORY"
		case "FACTORY_LINEAR_API_KEY":
			return "lin_api_config"
		case "FACTORY_LINEAR_WEBHOOK_SECRET":
			return "lin-whsec"
		case "FACTORY_DELIVERY_RETENTION":
			return "168h"
		case "FACTORY_SCHEDULE_CATCHUP":
			return "1h"
		case "FACTORY_GITHUB_API_BASE":
			return "http://127.0.0.1:9"
		default:
			return ""
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.server.GitHubAppID != 4242 || cfg.server.GitHubAppKey == nil || cfg.server.GitHubTokenMode != "app" {
		t.Fatalf("app id %d mode %s key nil %v", cfg.server.GitHubAppID, cfg.server.GitHubTokenMode, cfg.server.GitHubAppKey == nil)
	}
	if cfg.server.SlackTeamID != "T0FACTORY" || cfg.server.DeliveryRetention != 168*time.Hour || cfg.server.ScheduleCatchUp != time.Hour {
		t.Fatalf("team %s retention %s catchup %s", cfg.server.SlackTeamID, cfg.server.DeliveryRetention, cfg.server.ScheduleCatchUp)
	}
	if cfg.server.GitHubAPIBase != "http://127.0.0.1:9" {
		t.Fatal(cfg.server.GitHubAPIBase)
	}
	joined := strings.Join(cfg.secrets, "\n")
	if !strings.Contains(joined, webhook) || strings.Contains(string(pemBytes), webhook) {
		t.Fatal("webhook secret was not added to the redact list")
	}

	if _, err := configFromEnv(func(k string) string {
		if k == "FACTORY_GITHUB_TOKEN_MODE" {
			return "oauth"
		}
		return ""
	}, nil); err == nil || strings.Contains(err.Error(), webhook) {
		t.Fatalf("mode error %v", err)
	}
	if _, err := configFromEnv(func(k string) string {
		if k == "FACTORY_GITHUB_APP_ID" {
			return "1"
		}
		return ""
	}, nil); err == nil {
		t.Fatal("half app config was accepted")
	}
	if _, err := configFromEnv(func(k string) string {
		if k == "FACTORY_DELIVERY_RETENTION" {
			return "soon"
		}
		return ""
	}, nil); err == nil || strings.Contains(err.Error(), webhook) {
		t.Fatalf("duration error %v", err)
	}
}

func TestConsoleFlag(t *testing.T) {
	base := func(console string) func(string) string {
		return func(k string) string {
			switch k {
			case "FACTORY_DATABASE_URL":
				return "postgres://example"
			case "FACTORY_API_KEY":
				return "api-key-value"
			case "FACTORY_WORKER_API_KEY":
				return "worker-key-value"
			case "FACTORY_DEFINITION_PATH":
				return "/definitions"
			case "FACTORY_CONSOLE":
				return console
			default:
				return ""
			}
		}
	}
	cfg, err := configFromEnv(base(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.server.Console != "on" {
		t.Fatalf("default console %q", cfg.server.Console)
	}
	cfg, err = configFromEnv(base("off"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.server.Console != "off" {
		t.Fatalf("console %q", cfg.server.Console)
	}
	if _, err := configFromEnv(base("sometimes"), nil); err == nil || !strings.Contains(err.Error(), "FACTORY_CONSOLE") {
		t.Fatalf("error %v", err)
	}
}
