package store

import (
	"fmt"

	"github.com/jackc/pgx/v5"
)

// collect drains a list query the way the store's existing loops do.
// op is the error prefix, for example "store: list schedules".
// A query error, a scan error, and rows.Err are returned as op + ": " + err.
// No rows produces an empty slice.
func collect[T any](rows pgx.Rows, err error, op string, scan func(pgx.Row) (T, error)) ([]T, error) {
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		row, scanErr := scan(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("%s: %w", op, scanErr)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return out, nil
}
