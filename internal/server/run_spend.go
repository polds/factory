package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/store"
)

const spendBodyLimit = 1 << 20

var spendIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (s *Server) mountRunSpend(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/agent/runs/{runId}/spend:reserve", s.handleSpendReserve)
	mux.HandleFunc("POST /api/v1/agent/runs/{runId}/spend:settle", s.handleSpendSettle)
}

func (s *Server) spendPriceJSON() string {
	if strings.TrimSpace(s.cfg.SpendPrice) != "" {
		return s.cfg.SpendPrice
	}
	return strings.TrimSpace(os.Getenv(harness.EnvSpendPrice))
}

func (s *Server) attachSpend(run store.Run, env map[string]string) {
	if env == nil || env[harness.EnvBundleMode] != definition.BundleModeRoleFiles {
		return
	}
	price := strings.TrimSpace(s.spendPriceJSON())
	base := strings.TrimRight(s.cfg.TaskServerURL, "/")
	if price == "" || base == "" {
		return
	}
	parsed, err := harness.ParseSpendPrice(price)
	if err != nil {
		price = `{"models":[]}`
	} else if encoded, err := json.Marshal(parsed); err == nil {
		price = string(encoded)
	}
	spendURL := base + "/api/v1/agent/runs/" + run.ID + "/spend"
	env[harness.EnvSpendURL] = spendURL
	env[harness.EnvSpendPrice] = price
	env["FACTORY_SECRET_VALUES"] = appendSecret(env["FACTORY_SECRET_VALUES"], spendURL)
}

type spendSeat struct {
	TaskID       string
	RootID       string
	FactoryID    string
	InvocationID string
	Assignee     string
	Lead         string
	Status       string
	Ceiling      int64
	Shared       bool
}

func (s *Server) handleSpendReserve(w http.ResponseWriter, r *http.Request) {
	seat, token, ok := s.spendAuthorized(w, r)
	if !ok {
		return
	}
	var body struct {
		Cents     int64  `json:"cents"`
		CallID    string `json:"call_id"`
		AttemptID string `json:"attempt_id"`
	}
	if err := decodeSpend(w, r, &body); err != nil {
		writeSpend(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if !validSpendID(body.CallID, token) || !validSpendID(body.AttemptID, token) {
		writeSpend(w, http.StatusBadRequest, map[string]string{"error": "call id"})
		return
	}
	if seat.Status != store.NativeRunning {
		s.observeReserve(body.Cents, http.StatusConflict)
		writeSpend(w, http.StatusConflict, map[string]any{"error": "budget_denied"})
		return
	}
	wCents, priced := s.serverW()
	if !priced {
		s.observeReserve(body.Cents, http.StatusConflict)
		writeSpend(w, http.StatusConflict, map[string]string{"error": "unpriced"})
		return
	}
	if body.Cents <= 0 || body.Cents < wCents {
		s.observeReserve(body.Cents, http.StatusConflict)
		writeSpend(w, http.StatusConflict, map[string]any{"error": "budget_denied", "w": wCents})
		return
	}
	scopes := store.AncestorScopes(seat.FactoryID, seat.InvocationID, seat.Lead, seat.Assignee)
	out, err := coordination.ReserveAncestors(r.Context(), s.store, store.ReserveInput{
		InvocationID:   seat.InvocationID,
		TaskID:         seat.TaskID,
		IdempotencyKey: seat.TaskID + ":" + body.CallID + ":" + body.AttemptID,
		Cents:          body.Cents,
		ScopeIDs:       scopes,
		CeilingCents:   seat.Ceiling,
		SharedKey:      seat.Shared,
	})
	if err != nil {
		if errors.Is(err, store.ErrBudgetExceeded) {
			s.observeReserve(body.Cents, http.StatusConflict)
			writeSpend(w, http.StatusConflict, map[string]any{"error": "budget_denied", "w": wCents})
			return
		}
		s.logSpend("reserve", err)
		writeSpend(w, http.StatusInternalServerError, map[string]string{"error": "spend failed"})
		return
	}
	s.observeReserve(body.Cents, http.StatusOK)
	writeSpend(w, http.StatusOK, map[string]any{
		"reservation_id": out.ID,
		"cents":          out.Cents,
		"w":              wCents,
	})
}

func (s *Server) handleSpendSettle(w http.ResponseWriter, r *http.Request) {
	seat, token, ok := s.spendAuthorized(w, r)
	if !ok {
		return
	}
	var body struct {
		ReservationID      string `json:"reservation_id"`
		CallID             string `json:"call_id"`
		AttemptID          string `json:"attempt_id"`
		Cents              int64  `json:"cents"`
		MissingUsage       bool   `json:"missing_usage"`
		AllowanceExhausted bool   `json:"allowance_exhausted"`
	}
	if err := decodeSpend(w, r, &body); err != nil {
		writeSpend(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if !validSpendID(body.CallID, token) || !validSpendID(body.AttemptID, token) || strings.TrimSpace(body.ReservationID) == "" {
		writeSpend(w, http.StatusBadRequest, map[string]string{"error": "call id"})
		return
	}
	var reservedInv, reservedTask string
	var reservedCents int64
	err := s.db.QueryRow(r.Context(), `
		SELECT invocation_id, COALESCE(task_id, ''), cents
		FROM budget_reservations WHERE id = $1`, body.ReservationID).Scan(&reservedInv, &reservedTask, &reservedCents)
	if errors.Is(err, pgx.ErrNoRows) || reservedInv != seat.InvocationID || reservedTask != seat.TaskID {
		writeSpend(w, http.StatusForbidden, map[string]string{"error": "reservation"})
		return
	}
	if err != nil {
		s.logSpend("settle", err)
		writeSpend(w, http.StatusInternalServerError, map[string]string{"error": "spend failed"})
		return
	}
	charge := body.Cents
	if body.MissingUsage || body.AllowanceExhausted {
		if wCents, priced := s.serverW(); priced {
			charge = wCents
		} else if reservedCents > 0 {
			charge = reservedCents
		}
	}
	if charge < 0 {
		writeSpend(w, http.StatusBadRequest, map[string]string{"error": "cents"})
		return
	}
	out, err := coordination.SettleUsage(r.Context(), s.store, store.SettleInput{
		InvocationID:  seat.InvocationID,
		ReservationID: body.ReservationID,
		CallID:        seat.TaskID + ":" + body.CallID,
		AttemptID:     body.AttemptID,
		Cents:         charge,
		Failed:        body.MissingUsage || body.AllowanceExhausted,
	})
	if err != nil {
		s.logSpend("settle", err)
		writeSpend(w, http.StatusInternalServerError, map[string]string{"error": "spend failed"})
		return
	}
	pause := body.MissingUsage || body.AllowanceExhausted || out.QuarantinedCents > 0
	if pause {
		if _, err := s.CancelNativeSubtree(r.Context(), seat.FactoryID, seat.RootID, true); err != nil {
			s.logSpend("pause", err)
			writeSpend(w, http.StatusInternalServerError, map[string]string{"error": "spend failed"})
			return
		}
	}
	writeSpend(w, http.StatusOK, map[string]any{
		"counted_cents":     out.CountedCents,
		"quarantined_cents": out.QuarantinedCents,
		"duplicate":         out.Duplicate,
		"paused":            pause,
	})
}

func (s *Server) serverW() (int64, bool) {
	price, err := harness.ParseSpendPrice(s.spendPriceJSON())
	if err != nil {
		return 0, false
	}
	return price.WorstCaseCents()
}

func (s *Server) spendAuthorized(w http.ResponseWriter, r *http.Request) (spendSeat, string, bool) {
	token, ok := bearer(r)
	if !ok {
		writeSpend(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return spendSeat{}, "", false
	}
	runID := r.PathValue("runId")
	if _, matched, err := s.matchToken(r.Context(), runID, tokenHash(token)); err != nil || !matched {
		writeSpend(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return spendSeat{}, "", false
	}
	seat, err := s.loadSpendSeat(r.Context(), runID)
	if err != nil {
		writeSpend(w, http.StatusForbidden, map[string]string{"error": "reservation"})
		return spendSeat{}, "", false
	}
	return seat, token, true
}

func (s *Server) loadSpendSeat(ctx context.Context, runID string) (spendSeat, error) {
	var seat spendSeat
	err := s.db.QueryRow(ctx, `
		SELECT t.id, t.factory_id, t.invocation_id, t.assignee, t.task_status,
		       COALESCE(p.assignee, t.assignee)
		FROM native_activations a
		JOIN native_tasks t ON t.id = a.task_id
		LEFT JOIN native_tasks p ON p.id = t.parent_task_id
		WHERE a.run_id = $1
		ORDER BY a.fence DESC
		LIMIT 1`, runID).Scan(
		&seat.TaskID, &seat.FactoryID, &seat.InvocationID, &seat.Assignee, &seat.Status, &seat.Lead,
	)
	if err != nil {
		return spendSeat{}, err
	}
	err = s.db.QueryRow(ctx, `
		WITH RECURSIVE up AS (
		    SELECT id, parent_task_id FROM native_tasks WHERE id = $1
		    UNION ALL
		    SELECT t.id, t.parent_task_id
		    FROM native_tasks t
		    JOIN up ON t.id = up.parent_task_id
		)
		SELECT id FROM up WHERE parent_task_id IS NULL`, seat.TaskID).Scan(&seat.RootID)
	if err != nil {
		return spendSeat{}, err
	}
	err = s.db.QueryRow(ctx, `
		SELECT ceiling_cents, shared_key FROM native_policies WHERE invocation_id = $1`,
		seat.InvocationID).Scan(&seat.Ceiling, &seat.Shared)
	if err != nil {
		return spendSeat{}, err
	}
	return seat, nil
}

func validSpendID(id, token string) bool {
	if !spendIDPattern.MatchString(id) {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(id), []byte(token)) == 1 {
		return false
	}
	return true
}

func decodeSpend(w http.ResponseWriter, r *http.Request, dst any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, spendBodyLimit))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("empty")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	return dec.Decode(dst)
}

func writeSpend(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) observeReserve(cents int64, status int) {
	if s.reserveObserver != nil {
		s.reserveObserver(cents, status)
	}
}

func (s *Server) logSpend(op string, err error) {
	if s.log == nil || err == nil {
		return
	}
	s.log.Error("spend", "op", op, "err", err)
}
