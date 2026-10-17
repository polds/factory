package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeBuf(w, buf.Bytes())
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	writeProblemBody(w, Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	})
}

// writeProblemReason is the steer 409 body. reason is copied into both
// detail and reason so a problem client and a steer client see the same text.
func writeProblemReason(w http.ResponseWriter, status int, reason string) {
	writeProblemBody(w, Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: reason,
		Reason: reason,
	})
}

func writeProblemBody(w http.ResponseWriter, p Problem) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	writeBuf(w, buf.Bytes())
}

func writeBuf(w http.ResponseWriter, b []byte) {
	if _, err := w.Write(b); err != nil {
		return
	}
}

func (h *handler) writeBackendErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrConflict):
		writeProblem(w, http.StatusConflict, "run is terminal")
	case errors.Is(err, ErrUnauthorized):
		writeProblem(w, http.StatusUnauthorized, "unauthorized")
	default:
		writeProblem(w, http.StatusInternalServerError, "internal error")
	}
}
