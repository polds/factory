package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	fclient "github.com/polds/factory/internal/client"
	"github.com/polds/factory/internal/operator"
)

func main() {
	var metricsAddr, probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8082", "metrics bind address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "health probe bind address")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctrl.SetLogger(zap.New(zap.JSONEncoder()))

	if err := run(context.Background(), log, metricsAddr, probeAddr); err != nil {
		log.Error("operator stopped", "err", operatorRedact(err.Error()))
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, metricsAddr, probeAddr string) error {
	factoryURL := strings.TrimSpace(os.Getenv("FACTORY_URL"))
	if factoryURL == "" {
		return fmt.Errorf("FACTORY_URL is required")
	}
	apiKey := os.Getenv("FACTORY_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("FACTORY_API_KEY is required")
	}
	namespace := operatorNamespace()
	watches := watchNamespaces(namespace)
	webhookOn := envDefaultTrue("WEBHOOK_ENABLED")
	certManager := envBool("OPERATOR_CERT_MANAGER")

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	direct, err := client.New(cfg, client.Options{Scheme: operator.Scheme})
	if err != nil {
		return err
	}
	whOpts := webhook.Options{Port: 9443}
	if !webhookOn {
		whOpts.Port = -1
	}
	var issuer *operator.Issuer
	if webhookOn && !certManager {
		issuer = &operator.Issuer{
			Client:      direct,
			Namespace:   namespace,
			SecretName:  envOr("WEBHOOK_SECRET_NAME", "factory-operator-webhook"),
			WebhookName: envOr("WEBHOOK_CONFIG_NAME", "factory-operator-validating"),
			DNSNames:    dnsNames(envOr("WEBHOOK_SERVICE_NAME", "factory-operator-webhook"), namespace),
			Clock:       operator.RealClock{},
		}
		if err := issuer.Ensure(ctx); err != nil {
			return fmt.Errorf("webhook certificate: %s", operatorRedact(err.Error()))
		}
		whOpts.TLSOpts = []func(*tls.Config){
			func(cfg *tls.Config) {
				cfg.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
					return issuer.Certificate(ctx)
				}
			},
		}
	}
	if webhookOn && certManager {
		whOpts.CertDir = envOr("WEBHOOK_CERT_DIR", "/tmp/k8s-webhook-server/serving-certs")
	}

	namespaces := map[string]cache.Config{}
	for _, ns := range watches {
		namespaces[ns] = cache.Config{}
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme(),
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		WebhookServer:          webhook.NewServer(whOpts),
		Cache:                  cache.Options{DefaultNamespaces: namespaces},
		LeaderElection:         false,
	})
	if err != nil {
		return err
	}
	api := &fclient.Client{BaseURL: factoryURL, APIKey: apiKey}
	reconcilers := []interface{ SetupWithManager(ctrl.Manager) error }{
		&operator.FactoryReconciler{Client: mgr.GetClient(), API: api},
		&operator.AgentRunReconciler{Client: mgr.GetClient(), API: api, Base: ctx},
	}
	for _, reconciler := range reconcilers {
		if err := reconciler.SetupWithManager(mgr); err != nil {
			return err
		}
	}
	if webhookOn {
		if err := (&operator.Validator{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
			return err
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	log.Info("factory operator starting", "namespaces", strings.Join(watches, ","), "webhook", webhookOn, "certManager", certManager)
	return mgr.Start(ctx)
}

func scheme() *runtime.Scheme { return operator.Scheme }

func operatorNamespace() string {
	if v := strings.TrimSpace(os.Getenv("OPERATOR_NAMESPACE")); v != "" {
		return v
	}
	raw, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err == nil {
		if v := strings.TrimSpace(string(raw)); v != "" {
			return v
		}
	}
	return "default"
}

func watchNamespaces(fallback string) []string {
	raw := strings.TrimSpace(os.Getenv("WATCH_NAMESPACES"))
	if raw == "" {
		return []string{fallback}
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return []string{fallback}
	}
	return out
}

func dnsNames(service, namespace string) []string {
	return []string{
		service,
		service + "." + namespace + ".svc",
		service + "." + namespace + ".svc.cluster.local",
	}
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func envDefaultTrue(name string) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return true
	}
	return envBool(name)
}

func operatorRedact(s string) string {
	key := os.Getenv("FACTORY_API_KEY")
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[REDACTED]")
}
