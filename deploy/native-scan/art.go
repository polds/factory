package nativescan

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/polds/factory/internal/result"
)

const (
	retentionWindow    = 168 * time.Hour
	retainedReportFile = "report.json"
)

var itemIDRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// ErrItemID is a cleanup id outside the per-item root.
var ErrItemID = errors.New("nativescan: item id rejected")

// RetentionWindow is how long a published report is kept after workspace removal.
func RetentionWindow() time.Duration { return retentionWindow }

// PublishCandidate runs the publication guards, then the same writer commit uses.
// A failed guard leaves that writer's file absent.
func PublishCandidate(root, id string, authorized bool, body []byte, rels []string, now time.Time) error {
	if err := result.Publish(authorized, root, result.Report{
		Summary:        "scan",
		CoverageStatus: "recorded",
		FindingsCount:  0,
		Body:           body,
		ArtifactPaths:  rels,
	}); err != nil {
		return err
	}
	raw, err := json.Marshal(ReportView{
		ID:          id,
		Coverage:    "recorded",
		Body:        string(body),
		PublishedAt: now.UTC(),
	})
	if err != nil {
		return err
	}
	path, err := RetainedReportPath(root, id)
	if err != nil {
		return err
	}
	return writeRetainedReport(path, raw)
}

// RetainedReportPath is the file writeRetainedReport creates for one item.
func RetainedReportPath(root, id string) (string, error) {
	dir, err := retentionPath(root, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, retainedReportFile), nil
}

func writeRetainedReport(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// ExpireRetention removes reports whose published time is at least 168h before now.
func ExpireRetention(root string, now time.Time) error {
	matches, err := filepath.Glob(filepath.Join(root, "retention", "*", retainedReportFile))
	if err != nil {
		return err
	}
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var view ReportView
		if err := json.Unmarshal(raw, &view); err != nil {
			return err
		}
		if view.PublishedAt.IsZero() {
			continue
		}
		if !view.PublishedAt.Add(retentionWindow).After(now) {
			if err := os.RemoveAll(filepath.Dir(path)); err != nil {
				return err
			}
		}
	}
	return nil
}

func authorizeBearer(header, token string) bool {
	const prefix = "Bearer "
	if len(header) < len(prefix) || header[:len(prefix)] != prefix || token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(header[len(prefix):]))
	want := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(sum[:], want[:]) == 1
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func empty(w http.ResponseWriter, status int) {
	w.WriteHeader(status)
}

// Handler serves report and coverage after the task workspace is gone.
func (e *Engine) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/scans/{id}/report", func(w http.ResponseWriter, r *http.Request) {
		e.serveReport(w, r, token, true)
	})
	mux.HandleFunc("GET /api/v1/scans/{id}/coverage", func(w http.ResponseWriter, r *http.Request) {
		e.serveReport(w, r, token, false)
	})
	return mux
}

func (e *Engine) serveReport(w http.ResponseWriter, r *http.Request, token string, full bool) {
	if !authorizeBearer(r.Header.Get("Authorization"), token) {
		empty(w, http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	view, err := e.Get(id)
	if err != nil {
		empty(w, http.StatusNotFound)
		return
	}
	if !full {
		raw, err := json.Marshal(map[string]string{"status": view.Coverage})
		if err != nil {
			empty(w, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, raw)
		return
	}
	raw, err := json.Marshal(view)
	if err != nil {
		empty(w, http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, raw)
}

func (e *Engine) itemPath(id string) (string, error) {
	if !itemIDRe.MatchString(id) {
		return "", ErrItemID
	}
	items := filepath.Join(e.root, "items")
	full := filepath.Join(items, id)
	rel, err := filepath.Rel(items, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", ErrItemID
	}
	return full, nil
}

func (e *Engine) retentionDir(id string) (string, error) {
	return retentionPath(e.root, id)
}

func retentionPath(root, id string) (string, error) {
	if !itemIDRe.MatchString(id) {
		return "", ErrItemID
	}
	base := filepath.Join(root, "retention")
	full := filepath.Join(base, id)
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", ErrItemID
	}
	return full, nil
}
