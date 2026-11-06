package api

import (
	"errors"
	"net/http"

	"github.com/polds/factory/internal/invocation"
)

func (h *handler) handleCreateInvocation(w http.ResponseWriter, r *http.Request) {
	ib, ok := h.requireInvocation(w)
	if !ok {
		return
	}
	body, err := readLimited(w, r.Body, maxBodyBytes)
	if err != nil {
		writeReadErr(w, err)
		return
	}
	raw, err := invocation.RawMap(body)
	if err != nil {
		h.writeInvocationErr(w, err)
		return
	}
	req, err := invocation.DecodeRequest(raw)
	if err != nil {
		h.writeInvocationErr(w, err)
		return
	}
	principal := InvocationPrincipal{Kind: "api-key", Actor: APIKeyPrincipal}
	receipt, err := ib.Invoke(r.Context(), r.PathValue("uid"), toAPIRequest(req), principal)
	if err != nil {
		h.writeInvocationBackendErr(w, err)
		return
	}
	status := http.StatusCreated
	if receipt.Replay {
		status = http.StatusOK
	}
	writeJSON(w, status, receipt)
}

func (h *handler) handleGetInvocation(w http.ResponseWriter, r *http.Request) {
	ib, ok := h.requireInvocation(w)
	if !ok {
		return
	}
	receipt, err := ib.GetInvocation(r.Context(), r.PathValue("uid"), r.PathValue("invocationID"))
	if err != nil {
		h.writeInvocationBackendErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (h *handler) handleGetInvocationResult(w http.ResponseWriter, r *http.Request) {
	ib, ok := h.requireInvocation(w)
	if !ok {
		return
	}
	principal := InvocationPrincipal{Kind: "api-key", Actor: APIKeyPrincipal}
	result, err := ib.GetInvocationResult(r.Context(), r.PathValue("uid"), r.PathValue("invocationID"), principal)
	if err != nil {
		var pub resultUnauthorized
		if errors.As(err, &pub) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		h.writeInvocationBackendErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type resultUnauthorized struct{}

func (resultUnauthorized) Error() string { return "result_unauthorized" }

func (h *handler) requireInvocation(w http.ResponseWriter) (InvocationBackend, bool) {
	ib, ok := h.backend.(InvocationBackend)
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return ib, true
}

func (h *handler) writeInvocationErr(w http.ResponseWriter, err error) {
	var rej invocation.Reject
	if errors.As(err, &rej) {
		writeProblemReason(w, rej.Status, rej.Code)
		return
	}
	writeProblem(w, http.StatusBadRequest, "input_invalid")
}

func (h *handler) writeInvocationBackendErr(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrInvocationConflict) || errors.Is(err, invocation.ErrConflict) {
		writeProblemReason(w, http.StatusConflict, "invocation_conflict")
		return
	}
	var rej ErrInvocationRejected
	if errors.As(err, &rej) {
		writeProblemReason(w, rej.Status, rej.Code)
		return
	}
	var invRej invocation.Reject
	if errors.As(err, &invRej) {
		writeProblemReason(w, invRej.Status, invRej.Code)
		return
	}
	h.writeBackendErr(w, err)
}

func toAPIRequest(req invocation.Request) InvocationRequest {
	out := InvocationRequest{
		DefinitionRevision:  req.DefinitionRevision,
		Inputs:              req.Inputs,
		SourceOccurrenceKey: req.SourceOccurrenceKey,
		Intent:              req.Intent,
		Priority:            req.Priority,
	}
	if req.Profile.Name != "" {
		out.Profile = &InvocationProfile{Name: req.Profile.Name}
	}
	if req.Target != nil {
		out.Target = &InvocationTarget{
			Repository:  req.Target.Repository,
			Ref:         req.Target.Ref,
			PullRequest: req.Target.PullRequest,
		}
	}
	if req.Correlation != nil {
		out.Correlation = &InvocationCorr{
			ConversationID: req.Correlation.ConversationID,
			ReplyTo:        req.Correlation.ReplyTo,
			ThreadRef:      req.Correlation.ThreadRef,
		}
	}
	return out
}
