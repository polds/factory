package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/result"
	"github.com/polds/factory/internal/store"
)

// publishFinishReport stores a validated report before commitFinish marks the
// run succeeded. The row is written on tx, so reportMissing in that same
// transaction sees it, and a rollback keeps the success and the report together.
// An invocation-backed run with no report leaves the table empty.
func (s *Server) publishFinishReport(ctx context.Context, tx pgx.Tx, runID, status, summary string) error {
	if status != store.StatusSucceeded {
		return nil
	}
	var invocationID, factoryID string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(i.id, ''), COALESCE(i.factory_id, '')
		FROM runs r
		LEFT JOIN work_items w ON w.id = r.work_item_id
		LEFT JOIN invocations i ON i.id = w.invocation_id
		WHERE r.id = $1`, runID).Scan(&invocationID, &factoryID)
	if err != nil {
		return err
	}
	if invocationID == "" {
		return nil
	}
	rep, ok, err := loadFinishReport(ctx, tx, runID, summary)
	if err != nil || !ok {
		return err
	}
	pub := result.Report{
		Summary:        rep.Summary,
		CoverageStatus: rep.CoverageStatus,
		FindingsCount:  rep.FindingsCount,
		Body:           []byte(rep.Body),
		ArtifactPaths:  rep.ArtifactPaths,
	}
	if err := result.Publish(true, "", pub); err != nil {
		s.log.Error("publish report", "run_id", runID, "err", err)
	} else if result.ValidateTerminal(true, true, rep.FindingsCount, rep.CoverageStatus) == nil {
		return store.PutResultTx(ctx, tx, store.ResultRow{
			InvocationID:   invocationID,
			FactoryID:      factoryID,
			Summary:        rep.Summary,
			CoverageStatus: rep.CoverageStatus,
			FindingsCount:  rep.FindingsCount,
			ReportBody:     []byte(rep.Body),
			ArtifactPaths:  rep.ArtifactPaths,
			PublishedAt:    time.Now().UTC(),
		})
	}
	return nil
}

type finishReport struct {
	Summary        string
	CoverageStatus string
	FindingsCount  int
	Body           string
	ArtifactPaths  []string
}

func loadFinishReport(ctx context.Context, tx pgx.Tx, runID, summary string) (finishReport, bool, error) {
	rows, err := tx.Query(ctx, `SELECT payload_json FROM run_events WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		return finishReport{}, false, err
	}
	defer rows.Close()
	var found finishReport
	var ok bool
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return finishReport{}, false, err
		}
		if rep, parsed := parseFinishReport(blob); parsed {
			found = rep
			ok = true
		}
	}
	if err := rows.Err(); err != nil {
		return finishReport{}, false, err
	}
	if rep, parsed := parseFinishReport([]byte(summary)); parsed {
		return rep, true, nil
	}
	return found, ok, nil
}

// parseFinishReport reads a report document from a harness event or the
// completion message. Coverage must be present. findingsCount may be 0.
func parseFinishReport(raw []byte) (finishReport, bool) {
	if !json.Valid(raw) {
		return finishReport{}, false
	}
	var doc struct {
		Summary        string   `json:"summary"`
		CoverageStatus string   `json:"coverageStatus"`
		CoverageSnake  string   `json:"coverage_status"`
		FindingsCount  *int     `json:"findingsCount"`
		FindingsSnake  *int     `json:"findings_count"`
		Report         string   `json:"report"`
		ReportBody     string   `json:"report_body"`
		ArtifactPaths  []string `json:"artifactPaths"`
		ArtifactSnake  []string `json:"artifact_paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return finishReport{}, false
	}
	coverage := strings.TrimSpace(doc.CoverageStatus)
	if coverage == "" {
		coverage = strings.TrimSpace(doc.CoverageSnake)
	}
	if coverage == "" {
		return finishReport{}, false
	}
	findings := 0
	if doc.FindingsCount != nil {
		findings = *doc.FindingsCount
	} else if doc.FindingsSnake != nil {
		findings = *doc.FindingsSnake
	}
	body := doc.Report
	if body == "" {
		body = doc.ReportBody
	}
	if body == "" {
		body = doc.Summary
	}
	paths := doc.ArtifactPaths
	if len(paths) == 0 {
		paths = doc.ArtifactSnake
	}
	summary := doc.Summary
	if summary == "" {
		summary = body
	}
	return finishReport{
		Summary:        summary,
		CoverageStatus: coverage,
		FindingsCount:  findings,
		Body:           body,
		ArtifactPaths:  paths,
	}, true
}
