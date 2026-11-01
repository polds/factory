package operator

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/polds/factory/api/factory/v1alpha1"
	fclient "github.com/polds/factory/internal/client"
	"github.com/polds/factory/internal/convert"
	"github.com/polds/factory/internal/server"
)

func TestAcmeDefinitionAndRedaction(t *testing.T) {
	requireEnvtest(t)
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL is unset")
	}
	ctx := testContext(t)
	const planted = "sk-plantedFactorySecretValue99"
	var logs syncBuf
	apiClient := openFactoryd(t, &logs)

	ns := newNamespace(t, ctx)
	root := repoRoot(t)
	raw, err := convert.DirToManifests(filepath.Join(root, "examples", "acme-factory"), convert.Options{})
	if err != nil {
		t.Fatal(err)
	}
	applyManifests(t, ctx, ns, raw)

	rec := record.NewFakeRecorder(32)
	slogger := slog.New(slog.NewTextHandler(&logs, nil))
	ctrl.SetLogger(logr.FromSlogHandler(slogger.Handler()))
	reconciler := &FactoryReconciler{Client: k8sClient, API: apiClient, Recorder: rec, Log: logr.FromSlogHandler(slogger.Handler())}
	key := client.ObjectKey{Namespace: ns, Name: "acme-payments"}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var factory factoryv1.Factory
	if err := k8sClient.Get(ctx, key, &factory); err != nil {
		t.Fatal(err)
	}
	assertValid(t, &factory, true)
	view, err := apiClient.GetDefinition(ctx, "acme-payments")
	if err != nil {
		t.Fatal(err)
	}
	if !servedContains(view.Files, "name: acme-payments") {
		t.Fatal("factoryd is not serving the acme definition")
	}

	var factoryFile *factoryv1.SourceFile
	for i := range factory.Spec.Files {
		if factory.Spec.Files[i].Path == "factory.yaml" {
			factoryFile = &factory.Spec.Files[i]
			break
		}
	}
	if factoryFile == nil {
		t.Fatal("acme factory has no spec.files factory.yaml")
	}
	factoryFile.Content += "\n" + planted + ": true\n"
	if err := k8sClient.Update(ctx, &factory); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, key, &factory); err != nil {
		t.Fatal(err)
	}
	assertValid(t, &factory, false)
	rawStatus, err := json.Marshal(factory.Status)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawStatus, []byte(planted)) || !bytes.Contains(rawStatus, []byte("[REDACTED]")) {
		t.Fatal("status did not redact the diagnostic")
	}
	view, err = apiClient.GetDefinition(ctx, "acme-payments")
	if err != nil {
		t.Fatal(err)
	}
	if servedContains(view.Files, planted) || !servedContains(view.Files, "name: acme-payments") {
		t.Fatal("invalid push replaced the served definition")
	}
	drainEvents(t, rec, planted)
	if bytes.Contains(logs.Bytes(), []byte(planted)) {
		t.Fatal("logs leaked a secret-shaped value")
	}
}

func assertValid(t *testing.T, factory *factoryv1.Factory, valid bool) {
	t.Helper()
	want := metav1.ConditionFalse
	if valid {
		want = metav1.ConditionTrue
	}
	var found *metav1.Condition
	for i := range factory.Status.Conditions {
		if factory.Status.Conditions[i].Type == condDefinitionValid {
			found = &factory.Status.Conditions[i]
			break
		}
	}
	if found == nil || found.Status != want || found.ObservedGeneration == 0 {
		t.Fatalf("DefinitionValid %+v observed %d", found, factory.Status.ObservedGeneration)
	}
	if factory.Status.ObservedGeneration == 0 {
		t.Fatal("status.observedGeneration is unset")
	}
}

func servedContains(files []fclient.DefinitionFile, needle string) bool {
	for _, file := range files {
		if strings.Contains(file.Content, needle) {
			return true
		}
	}
	return false
}

func drainEvents(t *testing.T, rec *record.FakeRecorder, planted string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-rec.Events:
			if strings.Contains(ev, planted) {
				t.Fatal("event leaked a secret-shaped value")
			}
		case <-deadline:
			return
		}
	}
}

func applyManifests(t *testing.T, ctx context.Context, ns string, raw []byte) {
	t.Helper()
	dec := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var obj unstructured.Unstructured
		err := dec.Decode(&obj)
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(obj.Object) == 0 || obj.GetKind() == "" {
			continue
		}
		if obj.GetNamespace() == "" {
			obj.SetNamespace(ns)
		}
		if err := k8sClient.Create(ctx, &obj); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("create %s/%s: %v", obj.GetKind(), obj.GetName(), err)
		}
	}
}

func newNamespace(t *testing.T, ctx context.Context) string {
	t.Helper()
	var entropy [4]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	name := "t" + hex.EncodeToString(entropy[:])
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	return name
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..")
}

func openFactoryd(t *testing.T, logs io.Writer) *fclient.Client {
	t.Helper()
	ctx := context.Background()
	base := os.Getenv("FACTORY_TEST_DATABASE_URL")
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	schema := "t_" + hex.EncodeToString(entropy[:])
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	poolDSN, err := withSearchPath(base, schema, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE")
		_ = admin.Close(context.Background())
	})
	dir := t.TempDir()
	srv, err := server.Open(ctx, server.Config{
		DatabaseURL:      poolDSN,
		APIKey:           "test-api-key",
		WorkerAPIKey:     "test-worker-key",
		DefinitionPath:   dir,
		DefinitionSource: "api",
		Console:          "off",
		TaskServerURL:    "http://127.0.0.1:1",
		Log:              slog.New(slog.NewTextHandler(logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		httpSrv.Close()
		_ = srv.Close()
	})
	return &fclient.Client{
		BaseURL: httpSrv.URL,
		APIKey:  "test-api-key",
		Backoff: func(int) time.Duration { return 0 },
	}
}

func withSearchPath(raw, schema string, pool bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("search_path", schema)
	if pool {
		q.Set("pool_max_conns", "8")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}
