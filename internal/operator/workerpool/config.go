package workerpool

import "strings"

// Config is the operator's worker settings. Image, WebSocketURL and
// ServerRootURL are required; Missing names the first unset env var.
// Defaults match the chart: secret factory-worker / FACTORY_API_KEY,
// task namespace factory-runs, release name factory.
type Config struct {
	Image            string
	ImagePullPolicy  string
	WebSocketURL     string
	ServerRootURL    string
	APIKeySecretName string
	APIKeySecretKey  string
	TaskNamespaces   []string
	FullName         string
	AppName          string
	ReleaseName      string
	ImagePullSecrets []string
}

// Missing returns the env var the reconciler still needs, or "" when
// the pool can be rendered. The value itself is never included.
func (c Config) Missing() string {
	switch {
	case strings.TrimSpace(c.Image) == "":
		return "WORKER_IMAGE"
	case strings.TrimSpace(c.WebSocketURL) == "":
		return "WORKER_WEBSOCKET_URL"
	case strings.TrimSpace(c.ServerRootURL) == "":
		return "WORKER_SERVER_ROOT_URL"
	default:
		return ""
	}
}

func (c Config) allows(namespace string) bool {
	if namespace == "" {
		return false
	}
	for _, allowed := range c.TaskNamespaces {
		if allowed == namespace {
			return true
		}
	}
	return false
}

func (c Config) appName() string {
	if c.AppName != "" {
		return c.AppName
	}
	return "factory"
}

func (c Config) releaseName() string {
	if c.ReleaseName != "" {
		return c.ReleaseName
	}
	return "factory"
}

// releaseFullName mirrors factory.fullname: an explicit WORKER_FULLNAME,
// otherwise the release name when it already contains the chart name.
func (c Config) releaseFullName() string {
	if strings.TrimSpace(c.FullName) != "" {
		return truncDNS(c.FullName)
	}
	rel := c.releaseName()
	app := c.appName()
	if strings.Contains(rel, app) {
		return truncDNS(rel)
	}
	return truncDNS(rel + "-" + app)
}

func (c Config) objectName(poolName string) string {
	return truncDNS(c.releaseFullName() + "-worker-" + poolName)
}

func truncDNS(s string) string {
	if len(s) > 63 {
		s = s[:63]
	}
	return strings.TrimSuffix(s, "-")
}
