package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
)

// MarkdownFrontmatterNote is served on the schema index. Those resources have
// no JSON Schema document.
const MarkdownFrontmatterNote = "Markdown frontmatter resources (agent.md, automation.md, scorer.md, SKILL.md) are validated offline (layer 2), not by JSON Schema."

const currentVersion = "v1alpha1"

// Catalog is the body of GET /api/v1/factory-files/schemas.
type Catalog struct {
	Current             string   `json:"current"`
	Versions            []string `json:"versions"`
	MarkdownFrontmatter string   `json:"markdownFrontmatter"`
}

type handler struct {
	fsys fs.FS
	mux  *http.ServeMux
}

// NewHandler serves the unauthenticated schema routes from fsys.
// fsys is rooted at the schemas directory (one subdirectory per version).
//
//	GET /api/v1/factory-files/schemas
//	GET /api/v1/factory-files/schemas/{version}
//	GET /api/v1/factory-files/schemas/{version}/{document}
//
// Authorization is not read. The documents are a public contract so editors
// can validate a tree before anyone has a credential.
func NewHandler(fsys fs.FS) http.Handler {
	h := &handler{fsys: fsys, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /api/v1/factory-files/schemas", h.handleIndex)
	h.mux.HandleFunc("GET /api/v1/factory-files/schemas/{version}", h.handleVersion)
	h.mux.HandleFunc("GET /api/v1/factory-files/schemas/{version}/{document}", h.handleDocument)
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, pattern := h.mux.Handler(r); pattern == "" {
		if methods := allowedMethods(h.mux, r); len(methods) > 0 {
			w.Header().Set("Allow", strings.Join(methods, ", "))
			writeProblem(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) handleIndex(w http.ResponseWriter, _ *http.Request) {
	versions, err := h.versions()
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}
	current := ""
	for _, version := range versions {
		if version == currentVersion {
			current = version
			break
		}
	}
	writeJSON(w, http.StatusOK, Catalog{
		Current:             current,
		Versions:            versions,
		MarkdownFrontmatter: MarkdownFrontmatterNote,
	})
}

func (h *handler) handleVersion(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	if !validSegment(version) {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	docs, err := h.documents(version)
	if err != nil {
		writeFSErr(w, err)
		return
	}
	if docs == nil {
		docs = map[string]json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, docs)
}

func (h *handler) handleDocument(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	document := r.PathValue("document")
	if !validSegment(version) || !validSegment(document) || !strings.HasSuffix(document, ".json") {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	if h.fsys == nil {
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}
	body, err := fs.ReadFile(h.fsys, path.Join(version, document))
	if err != nil {
		writeFSErr(w, err)
		return
	}
	if !json.Valid(body) {
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *handler) versions() ([]string, error) {
	if h.fsys == nil {
		return nil, errors.New("no schema filesystem")
	}
	entries, err := fs.ReadDir(h.fsys, ".")
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validSegment(entry.Name()) {
			continue
		}
		docs, err := h.documents(entry.Name())
		if err != nil {
			return nil, err
		}
		if len(docs) == 0 {
			continue
		}
		versions = append(versions, entry.Name())
	}
	sort.Strings(versions)
	return versions, nil
}

func (h *handler) documents(version string) (map[string]json.RawMessage, error) {
	if h.fsys == nil {
		return nil, errors.New("no schema filesystem")
	}
	entries, err := fs.ReadDir(h.fsys, version)
	if err != nil {
		return nil, err
	}
	docs := make(map[string]json.RawMessage)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || !validSegment(name) {
			continue
		}
		body, err := fs.ReadFile(h.fsys, path.Join(version, name))
		if err != nil {
			return nil, err
		}
		if !json.Valid(body) {
			return nil, fmt.Errorf("schema %s/%s is not json", version, name)
		}
		docs[name] = json.RawMessage(body)
	}
	return docs, nil
}

func validSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func writeFSErr(w http.ResponseWriter, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	writeProblem(w, http.StatusInternalServerError, "internal error")
}

func allowedMethods(mux *http.ServeMux, r *http.Request) []string {
	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	}
	allowed := make([]string, 0, 1)
	for _, method := range methods {
		clone := r.Clone(r.Context())
		clone.Method = method
		if _, pattern := mux.Handler(clone); pattern != "" {
			allowed = append(allowed, method)
		}
	}
	return allowed
}
