package workerpool

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	yamlv2 "go.yaml.in/yaml/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
)

const (
	// LabelPool marks objects this reconciler created. Pre-existing objects
	// without it are never adopted or deleted.
	LabelPool = "factory.dev/workerpool"
	// AnnTaskNamespace records the task namespace a pool last rendered.
	// A namespace change deletes that namespace's Role and RoleBinding
	// before this annotation is overwritten.
	AnnTaskNamespace = "factory.dev/task-namespace"

	labelComponent  = "app.kubernetes.io/component"
	labelPartOf     = "app.kubernetes.io/part-of"
	labelName       = "app.kubernetes.io/name"
	labelInstance   = "app.kubernetes.io/instance"
	labelManagedBy  = "app.kubernetes.io/managed-by"
	labelPoolName   = "factory.dev/pool"
	managedBy       = "factory-operator"
	rbacProvenance  = "factory.dev/rbac-provenance"
	provenanceValue = "warpdotdev/oz-agent-worker/charts/oz-agent-worker/templates/role.yaml@63dbb8ae8ccb3941392691f5c628e3365d8e248e"
	workerContainer = "oz-agent-worker"
	configKey       = "config.yaml"
	configMount     = "/etc/oz-agent-worker"
	checksumAnn     = "checksum/config"
	graceSeconds    = int64(600)
	fixedLogLevel   = "info"
	metricsExporter = "none"
)

// Stack is the five objects _worker.tpl renders for one pool. The chart also
// renders a metrics Service and a preflight Job; those stay chart-only.
type Stack struct {
	ServiceAccount *corev1.ServiceAccount
	ConfigMap      *corev1.ConfigMap
	Deployment     *appsv1.Deployment
	Role           *rbacv1.Role
	RoleBinding    *rbacv1.RoleBinding
	// ConfigBody is the worker config the checksum is hashed from. It has no
	// trailing newline. The ConfigMap value is ConfigBody plus one newline,
	// which is what a Helm literal block stores.
	ConfigBody string
}

// Build renders the worker stack for pool. pool.Name is the chart worker
// name, pool label, and worker_id. Fields the chart renders from values that
// WorkerPool does not carry (cleanup, log level, metrics, use_image_volumes,
// deployment resources) use the chart defaults at 7b825c9.
func Build(pool *factoryv1.WorkerPool, cfg Config) (*Stack, error) {
	if pool == nil {
		return nil, fmt.Errorf("workerpool is nil")
	}
	body, err := configBody(pool)
	if err != nil {
		return nil, err
	}
	name := cfg.objectName(pool.Name)
	labels := objectLabels(cfg, pool)
	selector := selectorLabels(cfg, pool.Name)
	taskNS := ""
	if pool.Spec.KubernetesBackend != nil {
		taskNS = pool.Spec.KubernetesBackend.Namespace
	}
	sa := &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: meta(name, pool.Namespace, labels, nil),
	}
	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: meta(name+"-config", pool.Namespace, labels, nil),
		Data:       map[string]string{configKey: body + "\n"},
	}
	roleLabels := objectLabels(cfg, pool)
	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: meta(name, taskNS, roleLabels, map[string]string{
			rbacProvenance: provenanceValue,
		}),
		Rules: upstreamRules(),
	}
	binding := &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: meta(name, taskNS, objectLabels(cfg, pool), nil),
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      name,
			Namespace: pool.Namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     name,
		},
	}
	deploy := deployment(pool, cfg, name, labels, selector, body)
	return &Stack{
		ServiceAccount: sa,
		ConfigMap:      cm,
		Deployment:     deploy,
		Role:           role,
		RoleBinding:    binding,
		ConfigBody:     body,
	}, nil
}

func deployment(pool *factoryv1.WorkerPool, cfg Config, name string, labels, selector map[string]string, body string) *appsv1.Deployment {
	policy := corev1.PullPolicy(cfg.ImagePullPolicy)
	if policy == "" {
		policy = corev1.PullIfNotPresent
	}
	podLabels := map[string]string{}
	for k, v := range selector {
		podLabels[k] = v
	}
	var pulls []corev1.LocalObjectReference
	for _, secret := range cfg.ImagePullSecrets {
		if secret != "" {
			pulls = append(pulls, corev1.LocalObjectReference{Name: secret})
		}
	}
	replicas := pool.Spec.Replicas
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: meta(name, pool.Namespace, labels, nil),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
					Annotations: map[string]string{
						checksumAnn: sha256hex(body),
					},
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: i64p(graceSeconds),
					ServiceAccountName:            name,
					AutomountServiceAccountToken:  boolp(true),
					ImagePullSecrets:              pulls,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: boolp(true),
						RunAsUser:    i64p(10001),
						RunAsGroup:   i64p(10001),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{{
						Name:            workerContainer,
						Image:           cfg.Image,
						ImagePullPolicy: policy,
						Args: []string{
							"--config-file", configMount + "/" + configKey,
							"--backend", "kubernetes",
							"--log-level", fixedLogLevel,
							"--web-socket-url", cfg.WebSocketURL,
							"--server-root-url", cfg.ServerRootURL,
						},
						Env: []corev1.EnvVar{
							{
								Name: "WARP_API_KEY",
								ValueFrom: &corev1.EnvVarSource{
									SecretKeyRef: &corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{Name: cfg.APIKeySecretName},
										Key:                  cfg.APIKeySecretKey,
									},
								},
							},
							{Name: "OTEL_METRICS_EXPORTER", Value: metricsExporter},
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "config",
							MountPath: configMount,
							ReadOnly:  true,
						}},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								Exec: &corev1.ExecAction{Command: []string{"/bin/sh", "-c", "kill -0 1"}},
							},
							InitialDelaySeconds: 10,
							PeriodSeconds:       30,
							FailureThreshold:    3,
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: boolp(false),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: name + "-config"},
							},
						},
					}},
				},
			},
		},
	}
}

func meta(name, namespace string, labels, annotations map[string]string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:        name,
		Namespace:   namespace,
		Labels:      cloneMap(labels),
		Annotations: cloneMap(annotations),
	}
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func objectLabels(cfg Config, pool *factoryv1.WorkerPool) map[string]string {
	labels := selectorLabels(cfg, pool.Name)
	labels[labelManagedBy] = managedBy
	labels[labelPartOf] = "factory"
	labels[LabelPool] = PoolLabel(pool.Namespace, pool.Name)
	return labels
}

func selectorLabels(cfg Config, poolName string) map[string]string {
	return map[string]string{
		labelName:      cfg.appName(),
		labelInstance:  cfg.releaseName(),
		labelComponent: "worker",
		labelPoolName:  poolName,
	}
}

// PoolLabel is factory.dev/workerpool's value: "<namespace>.<name>", or a
// hash when that is not a legal label value.
func PoolLabel(namespace, name string) string {
	v := namespace + "." + name
	if labelValueOK(v) {
		return v
	}
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:16])
}

func labelValueOK(v string) bool {
	if v == "" || len(v) > 63 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
			if i == 0 || i == len(v)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func upstreamRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"batch"}, Resources: []string{"jobs"}, Verbs: []string{"create", "get", "list", "watch", "patch", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"pods/log"}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"list"}},
	}
}

func configBody(pool *factoryv1.WorkerPool) (string, error) {
	kb := pool.Spec.KubernetesBackend
	if kb == nil {
		kb = &factoryv1.KubernetesBackendSpec{}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "worker_id: %s\n", strconv.Quote(pool.Name))
	b.WriteString("cleanup: true\n")
	fmt.Fprintf(&b, "max_concurrent_tasks: %d\n", pool.Spec.MaxConcurrentTasks)
	b.WriteString("collect_task_logs: true\n")
	b.WriteString("backend:\n")
	b.WriteString("  kubernetes:\n")
	fmt.Fprintf(&b, "    namespace: %s\n", strconv.Quote(kb.Namespace))
	fmt.Fprintf(&b, "    image_pull_policy: %s\n", strconv.Quote(kb.ImagePullPolicy))
	b.WriteString("    use_image_volumes: false\n")
	fmt.Fprintf(&b, "    unschedulable_timeout: %s\n", strconv.Quote(kb.UnschedulableTimeout))
	if kb.DefaultImage != "" {
		fmt.Fprintf(&b, "    default_image: %s\n", strconv.Quote(kb.DefaultImage))
	}
	if len(kb.ExtraAnnotations) > 0 {
		raw, err := yamlv2.Marshal(kb.ExtraAnnotations)
		if err != nil {
			return "", fmt.Errorf("extra annotations: %w", err)
		}
		b.WriteString("    extra_annotations:")
		b.WriteString(nindent(6, string(raw)))
		b.WriteByte('\n')
	}
	if kb.ActiveDeadlineSeconds != 0 {
		fmt.Fprintf(&b, "    active_deadline_seconds: %d\n", kb.ActiveDeadlineSeconds)
	}
	if kb.TTLSecondsAfterFinished != 0 {
		fmt.Fprintf(&b, "    ttl_seconds_after_finished: %d\n", kb.TTLSecondsAfterFinished)
	}
	if kb.WorkspaceSizeLimit != "" {
		fmt.Fprintf(&b, "    workspace_size_limit: %s\n", strconv.Quote(kb.WorkspaceSizeLimit))
	}
	if kb.PodTemplate != nil && !podTemplateEmpty(kb.PodTemplate) {
		raw, err := yamlv2.Marshal(podTemplateValue(kb.PodTemplate))
		if err != nil {
			return "", fmt.Errorf("pod template: %w", err)
		}
		b.WriteString("    pod_template:")
		b.WriteString(nindent(6, string(raw)))
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

func podTemplateEmpty(p *factoryv1.WorkerPodTemplate) bool {
	return p.ServiceAccountName == "" && len(p.NodeSelector) == 0 && len(p.Containers) == 0
}

func podTemplateValue(p *factoryv1.WorkerPodTemplate) map[string]any {
	out := map[string]any{}
	if p.ServiceAccountName != "" {
		out["serviceAccountName"] = p.ServiceAccountName
	}
	if len(p.NodeSelector) > 0 {
		out["nodeSelector"] = p.NodeSelector
	}
	if len(p.Containers) > 0 {
		items := make([]any, 0, len(p.Containers))
		for _, c := range p.Containers {
			item := map[string]any{}
			if c.Name != "" {
				item["name"] = c.Name
			}
			if c.Resources != nil && (len(c.Resources.Requests) > 0 || len(c.Resources.Limits) > 0) {
				res := map[string]any{}
				if len(c.Resources.Limits) > 0 {
					res["limits"] = c.Resources.Limits
				}
				if len(c.Resources.Requests) > 0 {
					res["requests"] = c.Resources.Requests
				}
				item["resources"] = res
			}
			items = append(items, item)
		}
		out["containers"] = items
	}
	return out
}

func nindent(spaces int, raw string) string {
	raw = strings.TrimSuffix(raw, "\n")
	if raw == "" {
		return ""
	}
	pad := strings.Repeat(" ", spaces)
	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		b.WriteByte('\n')
		if line != "" {
			b.WriteString(pad)
			b.WriteString(line)
		}
	}
	return b.String()
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func boolp(v bool) *bool { return &v }

func i64p(v int64) *int64 { return &v }
