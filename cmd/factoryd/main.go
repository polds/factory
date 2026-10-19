package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/polds/factory/internal/server"
	"github.com/polds/factory/internal/store"
)

func main() {
	os.Exit(execute(os.Args[1:], os.Getenv, os.Environ(), os.Stdout, os.Stderr))
}

func execute(args []string, getenv func(string) string, environ []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return cmdServe(getenv, environ, stderr)
	}
	switch args[0] {
	case "serve":
		if len(args) != 1 {
			usage(stderr)
			return 2
		}
		return cmdServe(getenv, environ, stderr)
	case "migrate":
		return cmdMigrate(args[1:], getenv, stderr)
	case "healthcheck":
		if len(args) != 1 {
			usage(stderr)
			return 2
		}
		return cmdHealth(getenv, stderr)
	default:
		usage(stderr)
		return 2
	}
}

func cmdServe(getenv func(string) string, environ []string, stderr io.Writer) int {
	cfg, err := configFromEnv(getenv, environ)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err.Error())
		return 1
	}
	if err := validateServe(cfg.server); err != nil {
		_, _ = fmt.Fprintln(stderr, err.Error())
		return 1
	}
	cfg.server.Log = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.level}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s, err := server.Open(ctx, cfg.server)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "serve:", redact(err.Error(), cfg.secrets...))
		return 1
	}
	defer func() { _ = s.Close() }()

	httpSrv := &http.Server{
		Addr:              cfg.addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpSrv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutCtx); err != nil {
			_, _ = fmt.Fprintln(stderr, "shutdown:", redact(err.Error(), cfg.secrets...))
			return 1
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			_, _ = fmt.Fprintln(stderr, "serve:", redact(err.Error(), cfg.secrets...))
			return 1
		}
		return 0
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			_, _ = fmt.Fprintln(stderr, "serve:", redact(err.Error(), cfg.secrets...))
			return 1
		}
		return 0
	}
}

func cmdMigrate(args []string, getenv func(string) string, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "up" {
		usage(stderr)
		return 2
	}
	url := strings.TrimSpace(getenv("FACTORY_DATABASE_URL"))
	if url == "" {
		_, _ = fmt.Fprintln(stderr, "FACTORY_DATABASE_URL is required")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, url)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "migrate:", redact(err.Error(), url))
		return 1
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		_, _ = fmt.Fprintln(stderr, "migrate:", redact(err.Error(), url))
		return 1
	}
	return 0
}

func cmdHealth(getenv func(string) string, stderr io.Writer) int {
	raw := strings.TrimSpace(getenv("FACTORY_HEALTHCHECK_URL"))
	if raw == "" {
		raw = "http://127.0.0.1:8080/healthz"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "healthcheck:", err.Error())
		return 1
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "healthcheck:", err.Error())
		return 1
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintln(stderr, "healthcheck:", res.Status)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: factoryd [command]

Commands:
  serve            run the control plane (default when no command is given)
  migrate up       apply database migrations
  healthcheck      GET /healthz and exit non-zero unless it returns 200

Environment:
  FACTORY_DATABASE_URL
  FACTORY_API_KEY
  FACTORY_WORKER_API_KEY
  FACTORY_DEFINITION_PATH
  FACTORY_SCHEMAS_PATH
      Directory of schemas/<version>/*.json served without authentication.
      When unset, factoryd searches upward from its working directory.
  FACTORY_DEFINITION_GIT_URL
      Optional git remote. When set, factoryd polls it and applies a clean
      tree on the production ref. The local definition path stays the default
      and the last good tree is kept when validation fails.
  FACTORY_DEFINITION_GIT_REF (default main)
  FACTORY_DEFINITION_GIT_SUBDIR
  FACTORY_DEFINITION_GIT_PRODUCTION_REF
  FACTORY_DEFINITION_GIT_INTERVAL (default 1m)
  FACTORY_EXTERNAL_URL
  FACTORY_TASK_SERVER_URL
      Base URL task containers use to push events. Task containers are on
      the host Docker bridge, not the compose network, so they cannot use
      the compose service name. Default http://172.17.0.1:8080 (Linux
      bridge gateway, factoryd port published on the host). Docker Desktop
      uses http://host.docker.internal:8080.
  FACTORY_SECRET_<NAME>
  FACTORY_S3_ENDPOINT, FACTORY_S3_BUCKET, FACTORY_S3_ACCESS_KEY, FACTORY_S3_SECRET_KEY
  FACTORY_S3_FORCE_PATH_STYLE (default true), FACTORY_S3_REGION (default us-east-1)
  FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL, FACTORY_INFERENCE_OPENAI_API_KEY
  FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS
      Optional newline-separated "Name: value" lines sent on every
      request to the OpenAI-compatible endpoint. A matching name
      replaces the default Authorization or Content-Type header.
      Example: x-litellm-api-key: Bearer sk-...
  FACTORY_HTTP_ADDR (default :8080)
  FACTORY_HEALTHCHECK_URL (default http://127.0.0.1:8080/healthz)
  FACTORY_LOG_LEVEL
  FACTORY_GITHUB_APP_ID
  FACTORY_GITHUB_APP_PRIVATE_KEY_FILE
  FACTORY_GITHUB_WEBHOOK_SECRET
  FACTORY_GITHUB_TOKEN_MODE
      app mints a scoped installation token when a run allowlist includes
      GH_TOKEN. pat (default) copies FACTORY_SECRET_GH_TOKEN.
  FACTORY_SLACK_BOT_TOKEN
  FACTORY_SLACK_SIGNING_SECRET
  FACTORY_SLACK_TEAM_ID
      Team id used to bind slack:<team>/<channel>/<root ts>. The definition
      has a channel and no team id.
  FACTORY_LINEAR_API_KEY
  FACTORY_LINEAR_WEBHOOK_SECRET
  FACTORY_DELIVERY_RETENTION (default 168h)
  FACTORY_SCHEDULE_CATCHUP (default 1h)
  FACTORY_GITHUB_API_BASE
  FACTORY_SLACK_API_BASE
  FACTORY_LINEAR_API_BASE
`)
}

func redact(text string, secrets ...string) string {
	for _, secret := range secrets {
		if len(secret) < 4 {
			continue
		}
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}
