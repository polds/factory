package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ResultRow is durable report storage for an invocation.
type ResultRow struct {
	InvocationID   string
	FactoryID      string
	Summary        string
	CoverageStatus string
	FindingsCount  int
	ReportBody     []byte
	ArtifactPaths  []string
	PublishedAt    time.Time
}

// PutResultParams stores a validated report. The caller must run result.Publish first.
func (s *Store) PutResult(ctx context.Context, p ResultRow) error {
	paths, err := json.Marshal(p.ArtifactPaths)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO invocation_results (
		    invocation_id, factory_id, summary, coverage_status, findings_count,
		    report_body, artifact_paths, published_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (invocation_id) DO UPDATE SET
		    summary = EXCLUDED.summary,
		    coverage_status = EXCLUDED.coverage_status,
		    findings_count = EXCLUDED.findings_count,
		    report_body = EXCLUDED.report_body,
		    artifact_paths = EXCLUDED.artifact_paths,
		    published_at = EXCLUDED.published_at`,
		p.InvocationID, p.FactoryID, p.Summary, p.CoverageStatus, p.FindingsCount,
		p.ReportBody, paths, p.PublishedAt,
	)
	if err != nil {
		return fmt.Errorf("store: put result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("store: put result: no row")
	}
	return nil
}

// GetResult loads the durable report row.
func (s *Store) GetResult(ctx context.Context, factoryID, invocationID string) (ResultRow, error) {
	var row ResultRow
	var paths []byte
	err := s.pool.QueryRow(ctx, `
		SELECT invocation_id, factory_id, summary, coverage_status, findings_count,
		       report_body, artifact_paths, published_at
		FROM invocation_results
		WHERE factory_id = $1 AND invocation_id = $2`, factoryID, invocationID).Scan(
		&row.InvocationID, &row.FactoryID, &row.Summary, &row.CoverageStatus,
		&row.FindingsCount, &row.ReportBody, &paths, &row.PublishedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResultRow{}, ErrNotFound
	}
	if err != nil {
		return ResultRow{}, err
	}
	if len(paths) > 0 {
		_ = json.Unmarshal(paths, &row.ArtifactPaths)
	}
	return row, nil
}

// DeleteInvocationWorkspace is a no-op marker: results survive workspace cleanup.
func (s *Store) DeleteInvocationWorkspace(context.Context, string) error {
	return nil
}
