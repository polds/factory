package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxBody = 1 << 20

// server is the offline GitHub App API. It never writes the installation
// token, request headers, or request bodies to the log.
type server struct {
	log   *slog.Logger
	token string

	mu          sync.Mutex
	mints       []mintRecord
	checks      []checkRecord
	comments    []commentRecord
	nextComment int64
	pull        pullRecord
}

type mintRecord struct {
	Repositories []string          `json:"repositories"`
	Permissions  map[string]string `json:"permissions"`
}

type checkRecord struct {
	ID         int64  `json:"id"`
	Op         string `json:"op"`
	SHA        string `json:"sha,omitempty"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	Summary    string `json:"summary,omitempty"`
}

type commentRecord struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type commitRecord struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
}

type pullRecord struct {
	Number  int            `json:"number"`
	Commits []commitRecord `json:"commits"`
}

type stateDocument struct {
	TokenMints int             `json:"token_mints"`
	Mints      []mintRecord    `json:"mints"`
	Checks     []checkRecord   `json:"checks"`
	Comments   []commentRecord `json:"comments"`
	Pull       pullRecord      `json:"pull"`
}

func newServer(token string, log *slog.Logger) (*server, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if token == "" {
		var err error
		token, err = newInstallToken()
		if err != nil {
			return nil, err
		}
	}
	return &server{log: log, token: token, nextComment: 1}, nil
}

// newInstallToken returns the stateless installation-token shape GitHub
// has minted since 2026-10-02: ghs_<app id>_<header>.<payload>.<sig>.
// The value is about 520 characters and contains '.', '_', and '-'.
// A token passed to newServer is used as-is.
func newInstallToken() (string, error) {
	const header = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9"
	appID, err := statelessAppID()
	if err != nil {
		return "", err
	}
	payload, err := statelessPart(150)
	if err != nil {
		return "", err
	}
	sig, err := statelessPart(204)
	if err != nil {
		return "", err
	}
	return "ghs_" + appID + "_" + header + "." + payload + "." + sig, nil
}

func statelessAppID() (string, error) {
	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	n := int(buf[0])<<16 | int(buf[1])<<8 | int(buf[2])
	n = n%9000000 + 1000000
	return strconv.Itoa(n), nil
}

func statelessPart(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	part := []byte(base64.RawURLEncoding.EncodeToString(buf))
	if len(part) >= 2 {
		part[0] = '_'
		part[1] = '-'
	}
	return string(part), nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad request"})
		return
	}
	if len(body) > maxBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"message": "payload too large"})
		return
	}
	s.log.Info("github", "method", r.Method, "path", r.URL.Path)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		_, _ = io.WriteString(w, "ok\n")
	case r.Method == http.MethodGet && r.URL.Path == "/_spike/state":
		writeJSON(w, http.StatusOK, s.snapshot())
	case r.Method == http.MethodPost && r.URL.Path == "/_spike/commits":
		s.commit(w, r, body)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/installation"):
		s.installation(w, r)
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/access_tokens"):
		s.accessToken(w, r, body)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/check-runs"):
		s.createCheck(w, r, body)
	case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/check-runs/"):
		s.updateCheck(w, r, body)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
		s.listComments(w, r)
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
		s.createComment(w, r, body)
	case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/issues/comments/"):
		s.updateComment(w, r, body)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/collaborators/"):
		if !s.allowRepo(w, r) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
	}
}

func (s *server) installation(w http.ResponseWriter, r *http.Request) {
	if !s.allowApp(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": 7})
}

func (s *server) accessToken(w http.ResponseWriter, r *http.Request, body []byte) {
	if !s.allowApp(w, r) {
		return
	}
	var req struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad request"})
		return
	}
	s.mu.Lock()
	s.mints = append(s.mints, mintRecord{Repositories: append([]string(nil), req.Repositories...), Permissions: req.Permissions})
	token := s.token
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{
		"token":      token,
		"expires_at": time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339),
	})
}

func (s *server) createCheck(w http.ResponseWriter, r *http.Request, body []byte) {
	if !s.allowRepo(w, r) {
		return
	}
	var req struct {
		HeadSHA string `json:"head_sha"`
		Status  string `json:"status"`
		Output  struct {
			Summary string `json:"summary"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.HeadSHA == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad request"})
		return
	}
	s.mu.Lock()
	id := int64(1)
	for _, row := range s.checks {
		if row.Op == "create" {
			id = row.ID
			s.mu.Unlock()
			writeJSON(w, http.StatusCreated, map[string]any{"id": id})
			return
		}
	}
	s.checks = append(s.checks, checkRecord{
		ID: id, Op: "create", SHA: req.HeadSHA, Status: req.Status, Summary: req.Output.Summary,
	})
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *server) updateCheck(w http.ResponseWriter, r *http.Request, body []byte) {
	if !s.allowRepo(w, r) {
		return
	}
	id, ok := lastID(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
		return
	}
	var req struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Output     struct {
			Summary string `json:"summary"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Status == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad request"})
		return
	}
	s.mu.Lock()
	s.checks = append(s.checks, checkRecord{
		ID: id, Op: "update", Status: req.Status, Conclusion: req.Conclusion, Summary: req.Output.Summary,
	})
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *server) listComments(w http.ResponseWriter, r *http.Request) {
	if !s.allowRepo(w, r) {
		return
	}
	s.mu.Lock()
	out := append([]commentRecord(nil), s.comments...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *server) createComment(w http.ResponseWriter, r *http.Request, body []byte) {
	if !s.allowRepo(w, r) {
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad request"})
		return
	}
	s.mu.Lock()
	id := s.nextComment
	s.nextComment++
	s.comments = append(s.comments, commentRecord{ID: id, Body: req.Body})
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *server) updateComment(w http.ResponseWriter, r *http.Request, body []byte) {
	if !s.allowRepo(w, r) {
		return
	}
	id, ok := lastID(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad request"})
		return
	}
	s.mu.Lock()
	found := false
	for i := range s.comments {
		if s.comments[i].ID == id {
			s.comments[i].Body = req.Body
			found = true
		}
	}
	s.mu.Unlock()
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *server) commit(w http.ResponseWriter, r *http.Request, body []byte) {
	if !s.allowRepo(w, r) {
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad request"})
		return
	}
	s.mu.Lock()
	if s.pull.Number == 0 {
		s.pull.Number = 77
	}
	sum := sha256.Sum256([]byte(strconv.Itoa(len(s.pull.Commits)+1) + "\n" + req.Message))
	row := commitRecord{SHA: hex.EncodeToString(sum[:]), Message: req.Message}
	s.pull.Commits = append(s.pull.Commits, row)
	number := s.pull.Number
	count := len(s.pull.Commits)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"number":  number,
		"commits": count,
		"sha":     row.SHA,
	})
}

func (s *server) snapshot() stateDocument {
	s.mu.Lock()
	defer s.mu.Unlock()
	return stateDocument{
		TokenMints: len(s.mints),
		Mints:      append([]mintRecord(nil), s.mints...),
		Checks:     append([]checkRecord(nil), s.checks...),
		Comments:   append([]commentRecord(nil), s.comments...),
		Pull:       pullRecord{Number: s.pull.Number, Commits: append([]commitRecord(nil), s.pull.Commits...)},
	}
}

func (s *server) allowApp(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := bearer(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "unauthorized"})
		return false
	}
	return true
}

func (s *server) allowRepo(w http.ResponseWriter, r *http.Request) bool {
	got, ok := bearer(r)
	if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "unauthorized"})
		return false
	}
	return true
}

func bearer(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	token := strings.TrimSpace(value[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

func lastID(path string) (int64, bool) {
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "encode", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
