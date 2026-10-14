package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
	cfg := configFromEnv(func(k string) string {
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
