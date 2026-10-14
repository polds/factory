package main

import (
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/polds/factory/internal/artifacts"
	"github.com/polds/factory/internal/server"
)

type processConfig struct {
	server  server.Config
	addr    string
	level   slog.Level
	secrets []string
}

func configFromEnv(getenv func(string) string, environ []string) processConfig {
	secrets := secretsFrom(environ)
	hidden := make([]string, 0, len(secrets)+2)
	for _, value := range secrets {
		hidden = append(hidden, value)
	}
	if key := getenv("FACTORY_INFERENCE_OPENAI_API_KEY"); key != "" {
		hidden = append(hidden, key)
	}
	if key := getenv("FACTORY_API_KEY"); len(key) >= 4 {
		hidden = append(hidden, key)
	}
	if key := getenv("FACTORY_WORKER_API_KEY"); len(key) >= 4 {
		hidden = append(hidden, key)
	}
	if url := getenv("FACTORY_DATABASE_URL"); url != "" {
		hidden = append(hidden, url)
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
		},
		addr:    httpAddr(getenv),
		level:   logLevel(getenv("FACTORY_LOG_LEVEL")),
		secrets: hidden,
	}
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
