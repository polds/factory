package dreaminglive

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestFetchPinSendsCredentialAndRevokes(t *testing.T) {
	pinSrv := newPinServer(t, false)
	dest := filepath.Join(t.TempDir(), "dreaming-src")
	usePinServer(t, pinSrv)
	t.Setenv("FACTORY_ROLE_SOURCE", dest)
	if err := FetchPin(t.Context()); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", dest, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != pinSrv.pin {
		t.Fatalf("HEAD %s pin %s", got, pinSrv.pin)
	}
	cfg, err := gitPlainOutput(t.Context(), dest, "config", "--local", "--list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg, "credential") || strings.Contains(cfg, pinSrv.token) {
		t.Fatalf("checkout kept a credential: %s", cfg)
	}
	pinSrv.mu.Lock()
	defer pinSrv.mu.Unlock()
	if pinSrv.sawAuth != pinSrv.want {
		t.Fatalf("fetch did not present the credential: %q", pinSrv.sawAuth)
	}
	if pinSrv.revoked != 1 {
		t.Fatalf("revoke count %d", pinSrv.revoked)
	}
	if !strings.Contains(pinSrv.mintBody, `"contents":"read"`) || !strings.Contains(pinSrv.mintBody, `"dreaming"`) {
		t.Fatalf("mint body %s", pinSrv.mintBody)
	}
	if strings.Contains(pinSrv.mintBody, "write") {
		t.Fatalf("read token requested write: %s", pinSrv.mintBody)
	}
}

func TestFetchPinRevokeFailsClosed(t *testing.T) {
	pinSrv := newPinServer(t, true)
	usePinServer(t, pinSrv)
	t.Setenv("FACTORY_ROLE_SOURCE", filepath.Join(t.TempDir(), "dreaming-src"))
	err := FetchPin(t.Context())
	if err == nil || !strings.Contains(err.Error(), "revoke") {
		t.Fatalf("revoke failure: %v", err)
	}
}

type pinServer struct {
	url      string
	pin      string
	token    string
	want     string
	mu       sync.Mutex
	sawAuth  string
	mintBody string
	revoked  int
}

func usePinServer(t *testing.T, srv *pinServer) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	t.Setenv("M6_GITHUB_APP_ID", "4242")
	t.Setenv("M6_GITHUB_APP_PRIVATE_KEY", pemText)
	t.Setenv("FACTORY_GITHUB_API_BASE", srv.url)
	t.Setenv("DREAMING_PIN", srv.pin)
}

func newPinServer(t *testing.T, failRevoke bool) *pinServer {
	t.Helper()
	token := "ghs_read_pin_not_a_secret"
	project := t.TempDir()
	repo := filepath.Join(project, "northstar-ai-labs", "dreaming.git")
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, project, "init", "--bare", "-b", "main", repo)
	gitCmd(t, repo, "config", "uploadpack.allowReachableSHA1InWant", "true")
	gitCmd(t, repo, "config", "uploadpack.allowAnySHA1InWant", "true")
	src := t.TempDir()
	gitCmd(t, src, "init", "-b", "main")
	gitCmd(t, src, "config", "user.email", "dreaming@example.com")
	gitCmd(t, src, "config", "user.name", "dreaming")
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("pin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, src, "add", "f")
	gitCmd(t, src, "commit", "-m", "pin")
	out, err := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	pin := strings.TrimSpace(string(out))
	gitCmd(t, src, "push", repo, "HEAD:refs/heads/main")
	srv := &pinServer{
		pin:   pin,
		token: token,
		want:  "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)),
	}
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/northstar-ai-labs/dreaming/installation":
			_, _ = io.WriteString(w, `{"id":9,"repository_selection":"selected"}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/access_tokens"):
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			srv.mu.Lock()
			srv.mintBody = string(body)
			srv.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"token":%q}`, token)
		case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "auth", http.StatusUnauthorized)
				return
			}
			srv.mu.Lock()
			srv.revoked++
			srv.mu.Unlock()
			if failRevoke {
				http.Error(w, "revoke failed", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/northstar-ai-labs/dreaming.git/"):
			if r.Header.Get("Authorization") != srv.want {
				w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
				http.Error(w, "auth", http.StatusUnauthorized)
				return
			}
			srv.mu.Lock()
			srv.sawAuth = r.Header.Get("Authorization")
			srv.mu.Unlock()
			serveGitHTTP(w, r, project)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(httpSrv.Close)
	srv.url = httpSrv.URL
	return srv
}
