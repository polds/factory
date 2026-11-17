package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/store"
)

func TestDropAllowedRefusesHostileAndAdmin(t *testing.T) {
	good := "factory_ev_" + strings.Repeat("a", 32)
	if !dropAllowed(good, good, "factory", "postgres") {
		t.Fatal("expected a generated name to drop")
	}
	if dropAllowed(good, good, good, "postgres") {
		t.Fatal("admin database was a drop candidate")
	}
	if dropAllowed(good, good, "factory", good) {
		t.Fatal("admin role was a drop candidate")
	}
	if dropAllowed(`factory_ev_"; DROP DATABASE factory; --`, good, "factory", "postgres") {
		t.Fatal("hostile name was a drop candidate")
	}
}

func TestDisposableIsolation(t *testing.T) {
	s, raw, adminURL := openDisposableServer(t)
	ctx := context.Background()
	s.evidenceBundleForTest = true
	t.Setenv("FACTORY_EVIDENCE_PACKAGE", "./oracle")
	t.Setenv("FACTORY_EVIDENCE_MUTATION", "oracle/oracle.go")
	first, err := s.taskEnv(ctx, store.Run{ID: "run_iso_a", Prompt: "go test ./stolen"}, boundAgent{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.taskEnv(ctx, store.Run{ID: "run_iso_b", Prompt: "go test ./stolen"}, boundAgent{})
	if err != nil {
		t.Fatal(err)
	}
	if first[harness.EnvEvidencePackage] != "./oracle" || strings.Contains(first[harness.EnvEvidencePackage], "stolen") {
		t.Fatal("package came from the prompt")
	}
	if _, ok := first["FACTORY_DATABASE_URL"]; ok {
		t.Fatal("admin url key in task env")
	}
	if _, ok := first["FACTORY_DISPOSABLE_ADMIN_URL"]; ok {
		t.Fatal("admin url key in task env")
	}
	assertNoAdmin(t, joinEnv(first), adminURL)
	assertNoAdmin(t, joinEnv(second), adminURL)
	dsn := first[harness.EnvTestDSN]
	other := second[harness.EnvTestDSN]
	ownDB, ownRole := dsnIdentity(t, dsn)
	otherDB, _ := dsnIdentity(t, other)
	adminDB, adminRole := dsnIdentity(t, adminURL)
	if ownDB == adminDB || ownRole == adminRole || ownDB == otherDB || !storeName(ownDB) || !storeName(ownRole) {
		t.Fatal("task database did not differ from the admin database")
	}
	assertRole(t, raw, ownRole)
	conn := connectOK(t, dsn)
	if _, err := conn.Exec(ctx, `CREATE TABLE probe (id int)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO probe (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)
	for _, db := range []string{adminDB, "postgres", "template1", otherDB} {
		err := connectDB(ctx, dsn, db)
		if sqlState(err) != "42501" {
			t.Fatalf("connect %s state %s", db, sqlState(err))
		}
	}
	events := runEvidenceEvent(t, first, adminURL)
	if !strings.Contains(events, harness.EvidencePgvectorUnavailable) || strings.Contains(events, harness.EvidenceTestsPassed) {
		t.Fatal("pgvector was treated as success")
	}
	assertNoAdmin(t, events, adminURL)
	s.releaseDisposable(ctx, "run_iso_a")
	if err := connectDB(ctx, dsn, ownDB); err == nil {
		t.Fatal("connect succeeded after drop")
	}
	s.releaseDisposable(ctx, "run_iso_b")
}

func TestDisposableRetrySweepAndHostileName(t *testing.T) {
	s, raw, _ := openDisposableServer(t)
	ctx := context.Background()
	s.evidenceBundleForTest = true
	s.cfg.TokenRetrySleep = func(context.Context, time.Duration) error { return nil }
	var attempts int
	s.evidenceAttempt = func() error {
		attempts++
		if attempts == 1 {
			return &github.StatusError{Op: "mint", Method: "POST", Path: "/token", StatusCode: 503}
		}
		return nil
	}
	t.Setenv("FACTORY_EVIDENCE_PACKAGE", "./oracle")
	env, err := s.taskEnvRetry(ctx, store.Run{ID: "run_retry", Prompt: "go test ./stolen"}, boundAgent{})
	if err != nil || attempts != 2 {
		t.Fatalf("attempts %d err %v", attempts, err)
	}
	again, err := s.taskEnv(ctx, store.Run{ID: "run_retry", Prompt: "go test ./stolen"}, boundAgent{})
	if err != nil || again[harness.EnvTestDSN] != env[harness.EnvTestDSN] {
		t.Fatal("retry created a second dsn")
	}
	db, role := dsnIdentity(t, env[harness.EnvTestDSN])
	var roles int
	if err := raw.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname = $1`, role).Scan(&roles); err != nil || roles != 1 {
		t.Fatalf("roles %d %v", roles, err)
	}
	var rows int
	if err := raw.QueryRow(ctx, `SELECT count(*) FROM disposable_databases WHERE run_id = $1`, "run_retry").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows %d %v", rows, err)
	}

	if _, err := raw.Exec(ctx, `UPDATE disposable_databases SET state = 'ready', created_at = now() - interval '1 hour' WHERE run_id = $1`, "run_retry"); err != nil {
		t.Fatal(err)
	}
	if err := s.sweepDisposable(ctx); err != nil {
		t.Fatal(err)
	}
	if err := connectDB(ctx, env[harness.EnvTestDSN], db); err != nil {
		t.Fatal("restart dropped a ready database")
	}

	if _, err := raw.Exec(ctx, `UPDATE disposable_databases SET state = 'creating', created_at = now() WHERE run_id = $1`, "run_retry"); err != nil {
		t.Fatal(err)
	}
	if err := s.sweepDisposable(ctx); err != nil {
		t.Fatal(err)
	}
	if err := connectDB(ctx, env[harness.EnvTestDSN], db); err != nil {
		t.Fatal("sweep dropped a young creating database")
	}
	if _, err := raw.Exec(ctx, `UPDATE disposable_databases SET created_at = now() - interval '1 hour' WHERE run_id = $1`, "run_retry"); err != nil {
		t.Fatal(err)
	}
	if err := s.sweepDisposable(ctx); err != nil {
		t.Fatal(err)
	}
	if err := connectDB(ctx, env[harness.EnvTestDSN], db); err == nil {
		t.Fatal("stale creating database was kept")
	}

	execID := insertRunning(t, raw)
	execEnv, err := s.taskEnv(ctx, store.Run{ID: execID, Prompt: "go test ./stolen"}, boundAgent{})
	if err != nil {
		t.Fatal(err)
	}
	execDB, _ := dsnIdentity(t, execEnv[harness.EnvTestDSN])
	if _, err := raw.Exec(ctx, `UPDATE disposable_databases SET state = 'creating', created_at = now() - interval '1 hour' WHERE run_id = $1`, execID); err != nil {
		t.Fatal(err)
	}
	if err := s.sweepDisposable(ctx); err != nil {
		t.Fatal(err)
	}
	if err := connectDB(ctx, execEnv[harness.EnvTestDSN], execDB); err != nil {
		t.Fatal("sweep dropped a creating database for an executing run")
	}
	if _, err := raw.Exec(ctx, `UPDATE runs SET status = 'succeeded' WHERE id = $1`, execID); err != nil {
		t.Fatal(err)
	}
	if err := s.sweepDisposable(ctx); err != nil {
		t.Fatal(err)
	}
	if err := connectDB(ctx, execEnv[harness.EnvTestDSN], execDB); err == nil {
		t.Fatal("stale creating database survived after the run stopped")
	}

	canary := "d4keep" + randomHex(t, 4)
	if _, err := raw.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{canary}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = raw.Exec(context.Background(), `DROP DATABASE IF EXISTS `+pgx.Identifier{canary}.Sanitize())
	})
	hostile := `factory_ev_"; DROP DATABASE ` + canary + `; --`
	if _, err := raw.Exec(ctx, `
		INSERT INTO disposable_databases (run_id, database_name, role_name, state)
		VALUES ($1, $2, $2, 'dropping')`, "run_hostile", hostile); err != nil {
		t.Fatal(err)
	}
	s.releaseDisposable(ctx, "run_hostile")
	var kept int
	if err := raw.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname = $1`, canary).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("canary %d %v", kept, err)
	}
	var one int
	if err := raw.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatal("admin database was interpolated")
	}
}

func TestDisposablePrivilegeIsNotGreen(t *testing.T) {
	s, raw, adminURL := openDisposableServer(t)
	ctx := context.Background()
	role := "d4n" + randomHex(t, 4)
	pw := randomHex(t, 16)
	if _, err := raw.Exec(ctx, `CREATE ROLE `+pgx.Identifier{role}.Sanitize()+` LOGIN PASSWORD `+quoteLiteral(pw)+` NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = raw.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{role}.Sanitize())
	})
	db, _, err := postgresURLIdentity(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(ctx, `GRANT CONNECT ON DATABASE `+pgx.Identifier{db}.Sanitize()+` TO `+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	limited := swapUser(t, adminURL, role, pw)
	t.Setenv("FACTORY_DISPOSABLE_ADMIN_URL", limited)
	t.Setenv("FACTORY_EVIDENCE_PACKAGE", "./oracle")
	s.evidenceBundleForTest = true
	var before int
	if err := raw.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname LIKE 'factory_ev_%'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	env, err := s.taskEnv(ctx, store.Run{ID: "run_priv", Prompt: "go test ./stolen"}, boundAgent{})
	if err != nil {
		t.Fatal(err)
	}
	if env[harness.EnvEvidenceStatus] != harness.EvidencePrivilegeUnavailable || env[harness.EnvTestDSN] != "" {
		t.Fatal("privilege failure was green")
	}
	var after int
	if err := raw.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname LIKE 'factory_ev_%'`).Scan(&after); err != nil || after != before {
		t.Fatalf("roles %d %d %v", before, after, err)
	}
	assertNoAdmin(t, joinEnv(env), limited)
}

func TestDisposableDropUnblocksSameDatabase(t *testing.T) {
	s, raw, adminURL := openDisposableServer(t)
	ctx := context.Background()
	s.evidenceBundleForTest = true
	s.disposableDropFor = 500 * time.Millisecond
	t.Setenv("FACTORY_EVIDENCE_PACKAGE", "./oracle")
	env, err := s.taskEnv(ctx, store.Run{ID: "run_block", Prompt: "hold"}, boundAgent{})
	if err != nil {
		t.Fatal(err)
	}
	dsn := env[harness.EnvTestDSN]
	db, _ := dsnIdentity(t, dsn)
	hold, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Close(context.Background()) })
	if _, err := hold.Exec(ctx, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	live, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close(context.Background()) })
	var one int
	if err := live.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatal(err)
	}
	s.releaseDisposable(ctx, "run_block")
	if err := connectDB(ctx, dsn, db); err == nil {
		t.Fatal("blocked database was not dropped")
	}
	if err := live.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatal("drop terminated another database")
	}
	_ = raw
}

func openDisposableServer(t *testing.T) (*Server, *pgx.Conn, string) {
	t.Helper()
	base := disposableAdminURL(t)
	ctx := context.Background()
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	schema := "t_" + hex.EncodeToString(entropy[:])
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	poolDSN, err := withSearchPath(base, schema, true)
	if err != nil {
		t.Fatal(err)
	}
	rawDSN, err := withSearchPath(base, schema, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, poolDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		t.Fatal(err)
	}
	raw, err := pgx.Connect(ctx, rawDSN)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	s := &Server{
		cfg: Config{
			DatabaseURL:   poolDSN,
			TaskServerURL: "http://127.0.0.1:9",
		},
		store:               st,
		log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
		disposablePasswords: map[string]string{},
	}
	t.Cleanup(func() {
		dropDreamd(admin)
		restorePublic(admin)
		_ = raw.Close(context.Background())
		st.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE")
		_ = admin.Close(context.Background())
	})
	return s, raw, base
}

func disposableAdminURL(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("FACTORY_TEST_DATABASE_URL"); v != "" {
		return v
	}
	b, err := os.ReadFile("/tmp/d4-pg.url")
	if err != nil {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	return strings.TrimSpace(string(b))
}

func dropDreamd(admin *pgx.Conn) {
	ctx := context.Background()
	rows, err := admin.Query(ctx, `SELECT datname FROM pg_database WHERE datname LIKE 'factory_ev_%'`)
	if err != nil {
		return
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil && storeName(name) {
			names = append(names, name)
		}
	}
	rows.Close()
	for _, name := range names {
		_, _ = admin.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+pgx.Identifier{name}.Sanitize())
	}
	roles, err := admin.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname LIKE 'factory_ev_%'`)
	if err != nil {
		return
	}
	var roleNames []string
	for roles.Next() {
		var name string
		if err := roles.Scan(&name); err == nil && storeName(name) {
			roleNames = append(roleNames, name)
		}
	}
	roles.Close()
	for _, name := range roleNames {
		_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS `+pgx.Identifier{name}.Sanitize())
	}
}

func restorePublic(admin *pgx.Conn) {
	ctx := context.Background()
	for _, name := range []string{"factory", "postgres", "template1"} {
		_, _ = admin.Exec(ctx, `GRANT CONNECT ON DATABASE `+pgx.Identifier{name}.Sanitize()+` TO PUBLIC`)
	}
}

func assertNoAdmin(t *testing.T, blob, adminURL string) {
	t.Helper()
	if strings.Contains(blob, adminURL) || strings.Contains(blob, adminMarker(adminURL)) {
		t.Fatal("admin credential in task output")
	}
}

func adminMarker(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	pw, _ := u.User.Password()
	return u.User.Username() + ":" + pw + "@"
}

func joinEnv(env map[string]string) string {
	var b strings.Builder
	for k, v := range env {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte('\n')
	}
	return b.String()
}

func dsnIdentity(t *testing.T, raw string) (string, string) {
	t.Helper()
	db, role, err := postgresURLIdentity(raw)
	if err != nil {
		t.Fatal(err)
	}
	return db, role
}

func assertRole(t *testing.T, raw *pgx.Conn, role string) {
	t.Helper()
	var super, createdb, createrole, replication, bypass, login bool
	err := raw.QueryRow(context.Background(), `
		SELECT rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls, rolcanlogin
		FROM pg_roles WHERE rolname = $1`, role).Scan(&super, &createdb, &createrole, &replication, &bypass, &login)
	if err != nil || super || createdb || createrole || replication || bypass || !login {
		t.Fatal("role attributes")
	}
	var members int
	if err := raw.QueryRow(context.Background(), `
		SELECT count(*) FROM pg_auth_members m
		JOIN pg_roles r ON r.oid = m.member
		WHERE r.rolname = $1`, role).Scan(&members); err != nil || members != 0 {
		t.Fatal("role membership")
	}
}

func connectOK(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func connectDB(ctx context.Context, dsn, database string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return err
	}
	u.Path = "/" + database
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	var one int
	return conn.QueryRow(ctx, `SELECT 1`).Scan(&one)
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func runEvidenceEvent(t *testing.T, env map[string]string, adminURL string) string {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(srv.Close)
	runtime := t.TempDir()
	body := []byte(`{"prompt":"go test ./stolen","evidence_package":"./oracle"}` + "\n")
	if err := os.WriteFile(runtime+"/task.json", body, 0o600); err != nil {
		t.Fatal(err)
	}
	opt := harness.Options{
		Args:       []string{"agent", "run", "--task-id", "run_iso_a", "--model", "fake-model"},
		Env:        append(envSlice(env), "PATH="+os.Getenv("PATH"), "OPENAI_BASE_URL="+srv.URL),
		Workspace:  t.TempDir(),
		RuntimeDir: runtime,
	}
	code := harness.Run(context.Background(), opt)
	events, err := os.ReadFile(runtime + "/events.ndjson")
	if err != nil || code == 0 || calls.Load() != 0 {
		t.Fatalf("code %d calls %d err %v", code, calls.Load(), err)
	}
	assertNoAdmin(t, string(events), adminURL)
	return string(events)
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func insertRunning(t *testing.T, raw *pgx.Conn) string {
	t.Helper()
	ctx := context.Background()
	name := "d4fac" + randomHex(t, 4)
	var id string
	if err := raw.QueryRow(ctx, `
		INSERT INTO factories (id, name, schema_version, definition_source)
		VALUES ($1, $2, '1', 'path')
		RETURNING id`, "fac_"+name, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var runID string
	if err := raw.QueryRow(ctx, `
		INSERT INTO runs (id, factory_id, status, priority, pool)
		VALUES ($1, $2, 'running', 1, 'default')
		RETURNING id`, "run_"+name, id).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	return runID
}

func swapUser(t *testing.T, raw, role, password string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Del("pool_max_conns")
	q.Del("search_path")
	u.RawQuery = q.Encode()
	u.User = url.UserPassword(role, password)
	return u.String()
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(buf)
}
