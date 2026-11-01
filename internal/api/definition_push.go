package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// definitionPushMaxBytes is the largest definition bundle this route reads.
const definitionPushMaxBytes = 8 << 20

// definitionPushUnwiredDetail is the 501 problem detail when the Backend
// does not implement DefinitionPublisher.
const definitionPushUnwiredDetail = "definition push is not implemented"

// DefinitionSourceAPI is the GET /definition source of an API-backed factory.
const DefinitionSourceAPI = "api"

// ErrGitBackedDefinition is the 409 from PUT definition. A git URL or a
// path source both return it, and nothing is written.
var ErrGitBackedDefinition = errors.New("factory definition is git-backed")

// DefinitionPublisher is the Backend extension that publishes a definition
// bundle. Handlers type-assert Backend to it.
type DefinitionPublisher interface {
	PublishDefinition(ctx context.Context, name string, req DefinitionPushRequest) (DefinitionPushResponse, error)
}

// DefinitionPushRequest is the body of PUT /api/v1/factory/{name}/definition.
// Generation is the Factory metadata.generation the operator observed.
// Files are the definition tree. A git-backed factory is rejected with 409
// by the T89 implementation; the factory is either git-backed or API-backed.
type DefinitionPushRequest struct {
	Generation int64            `json:"generation"`
	Files      []DefinitionFile `json:"files"`
}

// DefinitionPushResponse is the validation result. PublishedGeneration is
// the generation left serving. Errors is empty when Valid is true.
type DefinitionPushResponse struct {
	Valid               bool         `json:"valid"`
	Errors              []Diagnostic `json:"errors"`
	PublishedGeneration int64        `json:"published_generation"`
}

func (h *handler) handlePutDefinition(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.PathValue("name")) == "" {
		writeProblem(w, http.StatusBadRequest, "factory name is required")
		return
	}
	body, err := readLimited(w, r.Body, definitionPushMaxBytes)
	if err != nil {
		writeReadErr(w, err)
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		writeProblem(w, http.StatusBadRequest, "request body is required")
		return
	}
	var req DefinitionPushRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Generation < 0 {
		writeProblem(w, http.StatusBadRequest, "generation must be >= 0")
		return
	}
	if len(req.Files) == 0 {
		writeProblem(w, http.StatusBadRequest, "files is required")
		return
	}
	for _, file := range req.Files {
		if strings.TrimSpace(file.Path) == "" {
			writeProblem(w, http.StatusBadRequest, "file path is required")
			return
		}
		if strings.HasPrefix(file.Path, "/") || strings.Contains(file.Path, "..") {
			writeProblem(w, http.StatusBadRequest, "file path must be relative")
			return
		}
	}
	pub, ok := h.backend.(DefinitionPublisher)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, definitionPushUnwiredDetail)
		return
	}
	resp, err := pub.PublishDefinition(r.Context(), strings.TrimSpace(r.PathValue("name")), req)
	if err != nil {
		if errors.Is(err, ErrGitBackedDefinition) {
			writeProblem(w, http.StatusConflict, "factory definition is git-backed")
			return
		}
		h.writeBackendErr(w, err)
		return
	}
	if resp.Errors == nil {
		resp.Errors = []Diagnostic{}
	}
	writeJSON(w, http.StatusOK, resp)
}
