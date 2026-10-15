package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const scheduleColumns = `
    id, factory_id, automation, name, cron, last_fired_at, next_fire_at, paused`

func scanSchedule(row pgx.Row) (Schedule, error) {
	var sch Schedule
	err := row.Scan(
		&sch.ID, &sch.FactoryID, &sch.Automation, &sch.Name, &sch.Cron,
		&sch.LastFiredAt, &sch.NextFireAt, &sch.Paused,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	if err != nil {
		return Schedule{}, err
	}
	return sch, nil
}

func validateSchedule(sch Schedule) error {
	if sch.FactoryID == "" {
		return fmt.Errorf("store: factory is required")
	}
	if sch.Automation == "" {
		return fmt.Errorf("store: automation is required")
	}
	if sch.Cron == "" {
		return fmt.Errorf("store: cron is required")
	}
	if sch.NextFireAt.IsZero() {
		return fmt.Errorf("store: next fire time is required")
	}
	return nil
}

// UpsertSchedule inserts a schedule or updates cron, next fire, and paused
// in place. last_fired_at is left as it was. An empty id is assigned.
func (s *Store) UpsertSchedule(ctx context.Context, sch Schedule) (Schedule, error) {
	if err := validateSchedule(sch); err != nil {
		return Schedule{}, err
	}
	if sch.ID == "" {
		id, err := NewID(PrefixSchedule)
		if err != nil {
			return Schedule{}, err
		}
		sch.ID = id
	}
	row, err := scanSchedule(s.pool.QueryRow(ctx, `
		INSERT INTO schedules (
		    id, factory_id, automation, name, cron, next_fire_at, paused)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (factory_id, automation, name) DO UPDATE
		SET cron = EXCLUDED.cron,
		    next_fire_at = COALESCE(EXCLUDED.next_fire_at, schedules.next_fire_at),
		    paused = EXCLUDED.paused
		RETURNING `+scheduleColumns,
		sch.ID, sch.FactoryID, sch.Automation, sch.Name, sch.Cron, nullTime(sch.NextFireAt), sch.Paused,
	))
	if err != nil {
		return Schedule{}, fmt.Errorf("store: upsert schedule: %w", err)
	}
	return row, nil
}

// ReplaceSchedule deletes the schedule named oldName for the same factory
// and automation, then upserts next. An old name equal to next.Name is an
// in-place upsert.
func (s *Store) ReplaceSchedule(ctx context.Context, oldName string, next Schedule) (Schedule, error) {
	if err := validateSchedule(next); err != nil {
		return Schedule{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Schedule{}, fmt.Errorf("store: replace schedule: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if oldName != next.Name {
		if _, err := tx.Exec(ctx, `
			DELETE FROM schedules
			WHERE factory_id = $1 AND automation = $2 AND name = $3`,
			next.FactoryID, next.Automation, oldName,
		); err != nil {
			return Schedule{}, fmt.Errorf("store: replace schedule: %w", err)
		}
	}
	if next.ID == "" {
		id, err := NewID(PrefixSchedule)
		if err != nil {
			return Schedule{}, err
		}
		next.ID = id
	}
	row, err := scanSchedule(tx.QueryRow(ctx, `
		INSERT INTO schedules (
		    id, factory_id, automation, name, cron, next_fire_at, paused)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (factory_id, automation, name) DO UPDATE
		SET cron = EXCLUDED.cron,
		    next_fire_at = COALESCE(EXCLUDED.next_fire_at, schedules.next_fire_at),
		    paused = EXCLUDED.paused
		RETURNING `+scheduleColumns,
		next.ID, next.FactoryID, next.Automation, next.Name, next.Cron, nullTime(next.NextFireAt), next.Paused,
	))
	if err != nil {
		return Schedule{}, fmt.Errorf("store: replace schedule: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Schedule{}, fmt.Errorf("store: replace schedule: %w", err)
	}
	return row, nil
}

// GetSchedule returns the schedule or ErrNotFound.
func (s *Store) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	row, err := scanSchedule(s.pool.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Schedule{}, err
		}
		return Schedule{}, fmt.Errorf("store: get schedule: %w", err)
	}
	return row, nil
}

// ListSchedules returns a factory's schedules, name order.
func (s *Store) ListSchedules(ctx context.Context, factoryID string) ([]Schedule, error) {
	if factoryID == "" {
		return nil, fmt.Errorf("store: factory is required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+scheduleColumns+`
		FROM schedules
		WHERE factory_id = $1
		ORDER BY automation ASC, name ASC, id ASC`, factoryID)
	if err != nil {
		return nil, fmt.Errorf("store: list schedules: %w", err)
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		row, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list schedules: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list schedules: %w", err)
	}
	return out, nil
}

// DueSchedules returns unpaused schedules whose next_fire_at is at or before now.
func (s *Store) DueSchedules(ctx context.Context, now time.Time) ([]Schedule, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("store: now is required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+scheduleColumns+`
		FROM schedules
		WHERE paused = FALSE
		  AND next_fire_at IS NOT NULL
		  AND next_fire_at <= $1
		ORDER BY next_fire_at ASC, id ASC`, now)
	if err != nil {
		return nil, fmt.Errorf("store: due schedules: %w", err)
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		row, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("store: due schedules: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: due schedules: %w", err)
	}
	return out, nil
}

// MarkFired records the fire time and the following next_fire_at.
func (s *Store) MarkFired(ctx context.Context, id string, firedAt, nextFire time.Time) error {
	if id == "" {
		return fmt.Errorf("store: schedule id is required")
	}
	if firedAt.IsZero() || nextFire.IsZero() {
		return fmt.Errorf("store: fire times are required")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE schedules
		SET last_fired_at = $2, next_fire_at = $3
		WHERE id = $1`, id, firedAt, nextFire)
	if err != nil {
		return fmt.Errorf("store: mark fired: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSchedulePaused pauses or resumes one schedule.
func (s *Store) SetSchedulePaused(ctx context.Context, id string, paused bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE schedules SET paused = $2 WHERE id = $1`, id, paused)
	if err != nil {
		return fmt.Errorf("store: pause schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSchedule removes the schedule for factoryID, automation, and name.
// Name may be empty. A missing row returns ErrNotFound.
// This is cron.Catalog.DeleteSchedule.
func (s *Store) DeleteSchedule(ctx context.Context, factoryID, automation, name string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM schedules
		WHERE factory_id = $1 AND automation = $2 AND name = $3`,
		factoryID, automation, name)
	if err != nil {
		return fmt.Errorf("store: delete schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteScheduleByID removes the schedule with id.
// A missing row returns ErrNotFound.
// DELETE /api/v1/agent/schedules/{id} deletes by id. cron.Catalog uses
// DeleteSchedule for the factory, automation, and name key.
func (s *Store) DeleteScheduleByID(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM schedules WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
