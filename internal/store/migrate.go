package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrateLockClass and migrateLockObj are the session advisory-lock key.
// Concurrent Migrate calls serialize on it, including across processes.
const (
	migrateLockClass int32 = 748293
	migrateLockObj   int32 = 1
)

// Migrate applies embedded migrations in order. It is idempotent: each file
// is recorded once in schema_migrations, and a second caller waits on the
// advisory lock instead of applying the same file.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: migrate acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1, $2)`, migrateLockClass, migrateLockObj); err != nil {
		return fmt.Errorf("store: migrate lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1, $2)`, migrateLockClass, migrateLockObj); err != nil {
			// A session lock survives Release. Close so the pool destroys this connection.
			_ = conn.Conn().Close(context.Background())
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
		)`); err != nil {
		return fmt.Errorf("store: schema_migrations: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		var applied bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE filename = $1)`, name).Scan(&applied); err != nil {
			return fmt.Errorf("store: migration status %s: %w", name, err)
		}
		if applied {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("store: read %s: %w", name, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("store: begin %s: %w", name, err)
		}
		if err := execScript(ctx, tx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("store: apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("store: record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("store: commit %s: %w", name, err)
		}
	}
	return nil
}

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("store: no migrations embedded")
	}
	return names, nil
}

func execScript(ctx context.Context, tx pgx.Tx, body string) error {
	for _, stmt := range splitSQL(body) {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// splitSQL splits a migration on semicolons that are outside quotes, comments,
// and dollar-quoted bodies. A PL/pgSQL function can keep its internal
// semicolons and still run as one statement.
func splitSQL(body string) []string {
	var b strings.Builder
	out := make([]string, 0)
	inSingle := false
	inLineComment := false
	inBlockComment := false
	dollar := ""
	for i := 0; i < len(body); {
		if inLineComment {
			b.WriteByte(body[i])
			if body[i] == '\n' {
				inLineComment = false
			}
			i++
			continue
		}
		if inBlockComment {
			if strings.HasPrefix(body[i:], "*/") {
				b.WriteString("*/")
				i += 2
				inBlockComment = false
				continue
			}
			b.WriteByte(body[i])
			i++
			continue
		}
		if dollar != "" {
			if strings.HasPrefix(body[i:], dollar) {
				b.WriteString(dollar)
				i += len(dollar)
				dollar = ""
				continue
			}
			b.WriteByte(body[i])
			i++
			continue
		}
		if inSingle {
			b.WriteByte(body[i])
			if body[i] == '\'' {
				if i+1 < len(body) && body[i+1] == '\'' {
					b.WriteByte('\'')
					i += 2
					continue
				}
				inSingle = false
			}
			i++
			continue
		}
		if strings.HasPrefix(body[i:], "--") {
			b.WriteString("--")
			i += 2
			inLineComment = true
			continue
		}
		if strings.HasPrefix(body[i:], "/*") {
			b.WriteString("/*")
			i += 2
			inBlockComment = true
			continue
		}
		if body[i] == '\'' {
			inSingle = true
			b.WriteByte(body[i])
			i++
			continue
		}
		if tag, next, ok := dollarTag(body, i); ok {
			dollar = tag
			b.WriteString(tag)
			i = next
			continue
		}
		if body[i] == ';' {
			if stmt := strings.TrimSpace(b.String()); stmt != "" {
				out = append(out, stmt)
			}
			b.Reset()
			i++
			continue
		}
		b.WriteByte(body[i])
		i++
	}
	if stmt := strings.TrimSpace(b.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

func dollarTag(body string, i int) (string, int, bool) {
	if i >= len(body) || body[i] != '$' {
		return "", 0, false
	}
	j := i + 1
	for j < len(body) {
		c := body[j]
		if c == '$' {
			return body[i : j+1], j + 1, true
		}
		if !isDollarTagChar(c) {
			return "", 0, false
		}
		j++
	}
	return "", 0, false
}

func isDollarTagChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
