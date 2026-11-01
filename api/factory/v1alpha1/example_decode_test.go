package v1alpha1

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	syaml "sigs.k8s.io/yaml"
)

func TestExampleResourcesDecode(t *testing.T) {
	path := filepath.Join("..", "..", "..", "deploy", "crds", "example-resources.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := decodeExample(body)
	if err != nil {
		t.Fatal(err)
	}
	if dec.namespace != "factory-payments" {
		t.Fatalf("namespace %q", dec.namespace)
	}
	if dec.factory.Name != "acme-payments" || dec.factory.Spec.Alias != "acme" {
		t.Fatalf("factory %+v", dec.factory.Name)
	}
	if dec.factory.Spec.AgentDefaults == nil || dec.factory.Spec.AgentDefaults.PoolRef == nil || dec.factory.Spec.AgentDefaults.PoolRef.Name != "standard" {
		t.Fatal("poolRef")
	}
	if dec.factory.Spec.Inference == nil || dec.factory.Spec.Inference.Default.CredentialRef != "anthropic-api-key" {
		t.Fatal("credentialRef")
	}
	if len(dec.agents) != 2 {
		t.Fatalf("agents %d", len(dec.agents))
	}
	var sawSecret bool
	for _, agent := range dec.agents {
		for _, ref := range agent.Spec.SecretRefs {
			if ref.Name == "sentry-auth-token" && ref.As == "SENTRY_AUTH_TOKEN" {
				sawSecret = true
			}
		}
	}
	if !sawSecret {
		t.Fatal("secretRef name missing")
	}
	if dec.webhook.Name != "" {
		t.Fatal("example has no webhook")
	}
	if dec.run.Spec.FactoryRef == nil || dec.run.Spec.AgentRef == nil || dec.run.Spec.Priority != "interactive" {
		t.Fatalf("agentrun %+v", dec.run.Spec)
	}
	if dec.pool.Spec.KubernetesBackend == nil || dec.pool.Spec.KubernetesBackend.UnschedulableTimeout != "10m" {
		t.Fatal("worker pool")
	}
}

type decodedExample struct {
	namespace string
	factory   Factory
	agents    []FactoryAgent
	run       AgentRun
	pool      WorkerPool
	webhook   FactoryWebhook
}

func decodeExample(body []byte) (decodedExample, error) {
	var out decodedExample
	docs := splitDocs(body)
	for _, doc := range docs {
		var meta struct {
			Kind string `json:"kind"`
		}
		if err := syaml.Unmarshal(doc, &meta); err != nil {
			return out, err
		}
		switch meta.Kind {
		case "Namespace":
			var ns corev1.Namespace
			if err := syaml.Unmarshal(doc, &ns); err != nil {
				return out, err
			}
			out.namespace = ns.Name
		case "Factory":
			if err := syaml.Unmarshal(doc, &out.factory); err != nil {
				return out, err
			}
		case "FactoryAgent":
			var agent FactoryAgent
			if err := syaml.Unmarshal(doc, &agent); err != nil {
				return out, err
			}
			out.agents = append(out.agents, agent)
		case "AgentRun":
			if err := syaml.Unmarshal(doc, &out.run); err != nil {
				return out, err
			}
		case "WorkerPool":
			if err := syaml.Unmarshal(doc, &out.pool); err != nil {
				return out, err
			}
		case "Runner":
			var runner Runner
			if err := syaml.Unmarshal(doc, &runner); err != nil {
				return out, err
			}
			if runner.Spec.FactoryRef == nil || runner.Spec.FactoryRef.Name == "" {
				return out, errMissingRef("Runner")
			}
		case "Automation":
			var auto Automation
			if err := syaml.Unmarshal(doc, &auto); err != nil {
				return out, err
			}
			if auto.Spec.AgentRef == nil {
				return out, errMissingRef("Automation")
			}
		case "Scorer":
			var scorer Scorer
			if err := syaml.Unmarshal(doc, &scorer); err != nil {
				return out, err
			}
			if len(scorer.Spec.Labels) == 0 {
				return out, errMissingRef("Scorer labels")
			}
		default:
		}
	}
	return out, nil
}

func splitDocs(body []byte) [][]byte {
	text := string(body)
	var docs [][]byte
	start := 0
	for i := 0; i < len(text); i++ {
		if (i == 0 || text[i-1] == '\n') && (text[i:] == "---" || (len(text[i:]) >= 4 && text[i:i+4] == "---\n")) {
			if i > start {
				part := trimDoc(text[start:i])
				if part != "" {
					docs = append(docs, []byte(part))
				}
			}
			if text[i:] == "---" {
				start = len(text)
				break
			}
			start = i + 4
		}
	}
	if start < len(text) {
		part := trimDoc(text[start:])
		if part != "" {
			docs = append(docs, []byte(part))
		}
	}
	return docs
}

func errMissingRef(kind string) error {
	return errors.New(kind + " missing a required field")
}

func trimDoc(s string) string {
	for len(s) > 0 && (s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	return s
}
