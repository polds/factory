package main

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/polds/factory/internal/artifacts"
	"github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/server"
)

type processConfig struct {
	server  server.Config
	addr    string
	level   slog.Level
	secrets []string
}

func configFromEnv(getenv func(string) string, environ []string) (processConfig, error) {
	secrets := secretsFrom(environ)
	hidden := make([]string, 0, len(secrets)+8)
	for _, value := range secrets {
		hidden = append(hidden, value)
	}
	hide := func(value string) {
		if len(value) >= 4 {
			hidden = append(hidden, value)
		}
	}
	hide(getenv("FACTORY_INFERENCE_OPENAI_API_KEY"))
	hide(getenv("FACTORY_API_KEY"))
	hide(getenv("FACTORY_WORKER_API_KEY"))
	if url := getenv("FACTORY_DATABASE_URL"); url != "" {
		hidden = append(hidden, url)
	}
	hide(getenv("FACTORY_GITHUB_WEBHOOK_SECRET"))
	hide(getenv("FACTORY_SLACK_BOT_TOKEN"))
	hide(getenv("FACTORY_SLACK_SIGNING_SECRET"))
	hide(getenv("FACTORY_LINEAR_API_KEY"))
	hide(getenv("FACTORY_LINEAR_WEBHOOK_SECRET"))

	appID, err := parseAppID(getenv("FACTORY_GITHUB_APP_ID"))
	if err != nil {
		return processConfig{}, err
	}
	key, err := loadAppKey(getenv("FACTORY_GITHUB_APP_PRIVATE_KEY_FILE"))
	if err != nil {
		return processConfig{}, err
	}
	mode, err := parseTokenMode(getenv("FACTORY_GITHUB_TOKEN_MODE"))
	if err != nil {
		return processConfig{}, err
	}
	if mode == "app" && (appID <= 0 || key == nil) {
		return processConfig{}, errors.New("FACTORY_GITHUB_TOKEN_MODE=app requires FACTORY_GITHUB_APP_ID and FACTORY_GITHUB_APP_PRIVATE_KEY_FILE")
	}
	if (appID > 0) != (key != nil) {
		return processConfig{}, errors.New("FACTORY_GITHUB_APP_ID and FACTORY_GITHUB_APP_PRIVATE_KEY_FILE must both be set")
	}
	retention, err := parseSetDuration("FACTORY_DELIVERY_RETENTION", getenv("FACTORY_DELIVERY_RETENTION"))
	if err != nil {
		return processConfig{}, err
	}
	catchup, err := parseSetDuration("FACTORY_SCHEDULE_CATCHUP", getenv("FACTORY_SCHEDULE_CATCHUP"))
	if err != nil {
		return processConfig{}, err
	}
	console, err := parseConsole(getenv("FACTORY_CONSOLE"))
	if err != nil {
		return processConfig{}, err
	}
	return processConfig{
		server: server.Config{
			DatabaseURL:         strings.TrimSpace(getenv("FACTORY_DATABASE_URL")),
			APIKey:              getenv("FACTORY_API_KEY"),
			WorkerAPIKey:        getenv("FACTORY_WORKER_API_KEY"),
			DefinitionPath:      strings.TrimSpace(getenv("FACTORY_DEFINITION_PATH")),
			ExternalURL:         strings.TrimSpace(getenv("FACTORY_EXTERNAL_URL")),
			TaskServerURL:       taskServerURL(getenv),
			HTTPAddr:            httpAddr(getenv),
			InferenceBaseURL:    strings.TrimSpace(getenv("FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL")),
			InferenceAPIKey:     getenv("FACTORY_INFERENCE_OPENAI_API_KEY"),
			Secrets:             secrets,
			WorkerPool:          strings.TrimSpace(getenv("FACTORY_WORKER_POOL")),
			WorkerPlatform:      strings.TrimSpace(getenv("FACTORY_WORKER_PLATFORM")),
			WorkerMaxConcurrent: atoiPositive(getenv("FACTORY_WORKER_MAX_CONCURRENT")),
			S3: artifacts.S3{
				Endpoint:  strings.TrimSpace(getenv("FACTORY_S3_ENDPOINT")),
				Bucket:    strings.TrimSpace(getenv("FACTORY_S3_BUCKET")),
				AccessKey: getenv("FACTORY_S3_ACCESS_KEY"),
				SecretKey: getenv("FACTORY_S3_SECRET_KEY"),
				Region:    s3Region(getenv),
				PathStyle: parseBoolDefault(getenv("FACTORY_S3_FORCE_PATH_STYLE"), true),
			},
			SchemasPath:                strings.TrimSpace(getenv("FACTORY_SCHEMAS_PATH")),
			DefinitionGitURL:           strings.TrimSpace(getenv("FACTORY_DEFINITION_GIT_URL")),
			DefinitionGitRef:           strings.TrimSpace(getenv("FACTORY_DEFINITION_GIT_REF")),
			DefinitionGitSubdir:        strings.TrimSpace(getenv("FACTORY_DEFINITION_GIT_SUBDIR")),
			DefinitionGitProductionRef: strings.TrimSpace(getenv("FACTORY_DEFINITION_GIT_PRODUCTION_REF")),
			DefinitionGitInterval:      parseDuration(getenv("FACTORY_DEFINITION_GIT_INTERVAL")),
			GitHubAppID:                appID,
			GitHubAppKey:               key,
			GitHubWebhookSecret:        getenv("FACTORY_GITHUB_WEBHOOK_SECRET"),
			GitHubTokenMode:            mode,
			GitHubAPIBase:              strings.TrimSpace(getenv("FACTORY_GITHUB_API_BASE")),
			SlackBotToken:              getenv("FACTORY_SLACK_BOT_TOKEN"),
			SlackSigningSecret:         getenv("FACTORY_SLACK_SIGNING_SECRET"),
			SlackTeamID:                strings.TrimSpace(getenv("FACTORY_SLACK_TEAM_ID")),
			SlackAPIBase:               strings.TrimSpace(getenv("FACTORY_SLACK_API_BASE")),
			LinearAPIKey:               getenv("FACTORY_LINEAR_API_KEY"),
			LinearWebhookSecret:        getenv("FACTORY_LINEAR_WEBHOOK_SECRET"),
			LinearAPIBase:              strings.TrimSpace(getenv("FACTORY_LINEAR_API_BASE")),
			DeliveryRetention:          retention,
			ScheduleCatchUp:            catchup,
			Console:                    console,
		},
		addr:    httpAddr(getenv),
		level:   logLevel(getenv("FACTORY_LOG_LEVEL")),
		secrets: hidden,
	}, nil
}

func validateServe(cfg server.Config) error {
	switch {
	case strings.TrimSpace(cfg.DatabaseURL) == "":
		return errors.New("FACTORY_DATABASE_URL is required")
	case cfg.APIKey == "":
		return errors.New("FACTORY_API_KEY is required")
	case cfg.WorkerAPIKey == "":
		return errors.New("FACTORY_WORKER_API_KEY is required")
	case strings.TrimSpace(cfg.DefinitionPath) == "":
		return errors.New("FACTORY_DEFINITION_PATH is required")
	default:
		return nil
	}
}

func parseConsole(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "on":
		return "on", nil
	case "off":
		return "off", nil
	default:
		return "", errors.New("FACTORY_CONSOLE must be on or off")
	}
}

func taskServerURL(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("FACTORY_TASK_SERVER_URL")); v != "" {
		return v
	}
	return server.DefaultTaskServerURL
}

func httpAddr(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("FACTORY_HTTP_ADDR")); v != "" {
		return v
	}
	return ":8080"
}

func s3Region(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("FACTORY_S3_REGION")); v != "" {
		return v
	}
	return "us-east-1"
}

func secretsFrom(environ []string) map[string]string {
	const prefix = "FACTORY_SECRET_"
	out := map[string]string{}
	for _, kv := range environ {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, prefix) {
			continue
		}
		name := strings.TrimPrefix(key, prefix)
		if name == "" {
			continue
		}
		out[name] = val
	}
	return out
}

func parseBoolDefault(raw string, fallback bool) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}

func parseAppID(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("FACTORY_GITHUB_APP_ID must be a positive integer")
	}
	return n, nil
}

func loadAppKey(path string) (*rsa.PrivateKey, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	key, err := github.LoadPrivateKey(path)
	if err != nil {
		return nil, errors.New("FACTORY_GITHUB_APP_PRIVATE_KEY_FILE is unreadable")
	}
	return key, nil
}

func parseTokenMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "pat":
		return "pat", nil
	case "app":
		return "app", nil
	default:
		return "", errors.New("FACTORY_GITHUB_TOKEN_MODE must be app or pat")
	}
}

func parseSetDuration(name, raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s is invalid", name)
	}
	return d, nil
}

func parseDuration(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

func atoiPositive(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func logLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
