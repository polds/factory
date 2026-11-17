package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/store"
)

const (
	disposableCreatingGrace = 2 * time.Minute
	disposableDropTimeout   = 3 * time.Second
)

var plainIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// attachEvidence gives a role_files evidence run its task DSN.
// The package and the mutation path come from the server environment.
// The prompt is not a command. The admin URL is not copied into env.
func (s *Server) attachEvidence(ctx context.Context, run store.Run, env map[string]string) error {
	if !s.evidenceRequested(env) {
		return nil
	}
	if s.evidenceBundleForTest {
		return s.attachHarnessEvidence(ctx, run, env, strings.TrimSpace(os.Getenv("FACTORY_EVIDENCE_PACKAGE")), strings.TrimSpace(os.Getenv("FACTORY_EVIDENCE_MUTATION")))
	}
	seat, err := s.classifySeat(ctx, run)
	if err != nil {
		return err
	}
	if seat.kind != "reviewer" {
		return nil
	}
	return s.attachReviewerEvidence(ctx, run, env, seat)
}

func (s *Server) attachReviewerEvidence(ctx context.Context, run store.Run, env map[string]string, seat seatClass) error {
	row, err := s.store.GetProducedHead(ctx, seat.taskID)
	if err != nil || row.State != store.ProducedReady || !commitHex40(row.HeadSHA) {
		return errors.New("produced head: reviewer: missing SHA")
	}
	pkg, mut, err := s.frozenEvidence(ctx, run)
	if err != nil {
		return err
	}
	if pkg == "" {
		return errors.New("produced head: reviewer: FACTORY_EVIDENCE_PACKAGE unset")
	}
	if mut == "" {
		return errors.New("produced head: reviewer: FACTORY_EVIDENCE_MUTATION unset")
	}
	if err := harness.EvidencePackagePath(pkg); err != nil {
		return err
	}
	if err := harness.EvidenceMutationPath(mut); err != nil {
		return err
	}
	ovs, err := s.invocationCheckoutOverrides(ctx, run.WorkItemID)
	if err != nil || len(ovs) != 1 || ovs[0].RepoOwner == "" || ovs[0].RepoName == "" {
		return errors.New("produced head: reviewer: repository missing")
	}
	env[harness.EnvEvidencePackage] = pkg
	env[harness.EnvEvidenceMutation] = mut
	env[harness.EnvReviewHead] = row.HeadSHA
	env[harness.EnvOriginURL] = "https://github.com/" + ovs[0].RepoOwner + "/" + ovs[0].RepoName + ".git"
	base := strings.TrimRight(s.cfg.TaskServerURL, "/")
	env[harness.EnvBundleURL] = base + "/api/v1/agent/runs/" + run.ID + "/head.bundle"
	if s.store == nil || run.ID == "" {
		return errors.New("server: disposable run")
	}
	dsn, priv, err := s.ensureDisposable(ctx, run.ID)
	if err != nil {
		return err
	}
	if priv {
		env[harness.EnvEvidenceStatus] = harness.EvidencePrivilegeUnavailable
		return nil
	}
	env[harness.EnvTestDSN] = dsn
	for _, needle := range harness.RedactionNeedles([]string{dsn}) {
		env["FACTORY_SECRET_VALUES"] = appendSecret(env["FACTORY_SECRET_VALUES"], needle)
	}
	return nil
}

func (s *Server) frozenEvidence(ctx context.Context, run store.Run) (string, string, error) {
	if s.store == nil || run.WorkItemID == "" {
		return "", "", nil
	}
	item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
	if err != nil || item.InvocationID == "" {
		return "", "", err
	}
	frozen, err := s.store.FrozenRun(ctx, item.InvocationID)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(frozen.Package), strings.TrimSpace(frozen.Mutation), nil
}

func (s *Server) attachHarnessEvidence(ctx context.Context, run store.Run, env map[string]string, pkg, mut string) error {
	if pkg == "" {
		return nil
	}
	if err := harness.EvidencePackagePath(pkg); err != nil {
		return err
	}
	if err := harness.EvidenceMutationPath(mut); err != nil {
		return err
	}
	env[harness.EnvEvidencePackage] = pkg
	if mut != "" {
		env[harness.EnvEvidenceMutation] = mut
	}
	if s.store == nil || run.ID == "" {
		return errors.New("server: disposable run")
	}
	dsn, priv, err := s.ensureDisposable(ctx, run.ID)
	if err != nil {
		return err
	}
	if priv {
		env[harness.EnvEvidenceStatus] = harness.EvidencePrivilegeUnavailable
		return nil
	}
	env[harness.EnvTestDSN] = dsn
	for _, needle := range harness.RedactionNeedles([]string{dsn}) {
		env["FACTORY_SECRET_VALUES"] = appendSecret(env["FACTORY_SECRET_VALUES"], needle)
	}
	return nil
}

func (s *Server) evidenceRequested(env map[string]string) bool {
	if s.evidenceBundleForTest {
		return true
	}
	return env[harness.EnvBundleMode] == definition.BundleModeRoleFiles
}

func (s *Server) disposableAdminURL() string {
	if v := strings.TrimSpace(os.Getenv("FACTORY_DISPOSABLE_ADMIN_URL")); v != "" {
		return v
	}
	return s.cfg.DatabaseURL
}

// ensureDisposable inserts names before DDL and reuses that one row.
// priv is true when CREATE ROLE or CREATE DATABASE is denied. That result
// is not a successful evidence run.
func (s *Server) ensureDisposable(ctx context.Context, runID string) (string, bool, error) {
	s.disposableOp.Lock()
	defer s.disposableOp.Unlock()
	row, err := s.store.GetDisposable(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		name, nerr := newDisposableName()
		if nerr != nil {
			return "", false, nerr
		}
		pw, nerr := newDisposablePassword()
		if nerr != nil {
			return "", false, nerr
		}
		inserted, created, ierr := s.store.InsertDisposable(ctx, runID, name, name)
		if ierr != nil {
			return "", false, ierr
		}
		row = inserted
		if created {
			s.rememberPassword(runID, pw)
		}
	} else if err != nil {
		return "", false, err
	}
	if row.State == store.DisposableDropped || row.State == store.DisposableDropping {
		return "", false, errors.New("server: disposable database is not available")
	}
	pw := s.password(runID)
	if pw == "" {
		if row.State == store.DisposableReady {
			exec, rerr := s.store.RunExecuting(ctx, runID)
			if rerr != nil {
				return "", false, rerr
			}
			if exec {
				return "", false, errors.New("server: disposable password is not in this process")
			}
		}
		pw, err = newDisposablePassword()
		if err != nil {
			return "", false, err
		}
		s.rememberPassword(runID, pw)
	}
	if err := s.provision(ctx, row, pw); err != nil {
		if errors.Is(err, errDisposablePrivilege) {
			return "", true, nil
		}
		return "", false, err
	}
	if err := s.store.MarkDisposable(ctx, runID, store.DisposableReady); err != nil {
		return "", false, err
	}
	dsn, err := taskDSN(s.disposableAdminURL(), row.RoleName, pw, row.DatabaseName)
	if err != nil {
		return "", false, err
	}
	return dsn, false, nil
}

var errDisposablePrivilege = errors.New("server: disposable privilege")

func (s *Server) provision(ctx context.Context, row store.Disposable, password string) error {
	if !dropAllowed(row.DatabaseName, row.RoleName, "", "") {
		return errors.New("server: disposable name")
	}
	adminURL, err := stripPoolQuery(s.disposableAdminURL())
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return safePG(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	dbName, roleName, err := postgresURLIdentity(adminURL)
	if err != nil {
		return err
	}
	if err := s.restrictConnect(ctx, conn, roleName, dbName); err != nil {
		return err
	}
	if err := syncRole(ctx, conn, row.RoleName, password); err != nil {
		return err
	}
	if err := createDatabase(ctx, conn, row.DatabaseName); err != nil {
		return err
	}
	if err := grantDisposable(ctx, conn, row.DatabaseName, row.RoleName); err != nil {
		return err
	}
	own, err := swapDatabase(adminURL, row.DatabaseName)
	if err != nil {
		return err
	}
	owned, err := pgx.Connect(ctx, own)
	if err != nil {
		return safePG(err)
	}
	defer func() { _ = owned.Close(ctx) }()
	ident := pgx.Identifier{row.RoleName}.Sanitize()
	if _, err := owned.Exec(ctx, "GRANT USAGE, CREATE ON SCHEMA public TO "+ident); err != nil {
		return safePG(err)
	}
	return nil
}

func (s *Server) restrictConnect(ctx context.Context, conn *pgx.Conn, adminRole, adminDB string) error {
	names := []string{adminDB, "postgres", "template1"}
	if other, _, err := postgresURLIdentity(s.cfg.DatabaseURL); err == nil && other != "" {
		names = append(names, other)
	}
	seen := map[string]struct{}{}
	if !plainIdent.MatchString(adminRole) {
		return errors.New("server: disposable admin role")
	}
	role := pgx.Identifier{adminRole}.Sanitize()
	for _, name := range names {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if !plainIdent.MatchString(name) {
			return errors.New("server: disposable admin database")
		}
		ident := pgx.Identifier{name}.Sanitize()
		if _, err := conn.Exec(ctx, "GRANT CONNECT ON DATABASE "+ident+" TO "+role); err != nil {
			return safePG(err)
		}
		if _, err := conn.Exec(ctx, "REVOKE CONNECT ON DATABASE "+ident+" FROM PUBLIC"); err != nil {
			return safePG(err)
		}
	}
	return nil
}

func syncRole(ctx context.Context, conn *pgx.Conn, role, password string) error {
	ident := pgx.Identifier{role}.Sanitize()
	stmt := "CREATE ROLE " + ident + " LOGIN PASSWORD " + quoteLiteral(password) +
		" NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"
	if _, err := conn.Exec(ctx, stmt); err != nil {
		if pgCode(err) != "42710" {
			return safePG(err)
		}
		alter := "ALTER ROLE " + ident + " WITH PASSWORD " + quoteLiteral(password)
		if _, aerr := conn.Exec(ctx, alter); aerr != nil {
			return safePG(aerr)
		}
	}
	return nil
}

func createDatabase(ctx context.Context, conn *pgx.Conn, name string) error {
	_, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	if err == nil || pgCode(err) == "42P04" {
		return nil
	}
	return safePG(err)
}

func grantDisposable(ctx context.Context, conn *pgx.Conn, database, role string) error {
	db := pgx.Identifier{database}.Sanitize()
	who := pgx.Identifier{role}.Sanitize()
	if _, err := conn.Exec(ctx, "REVOKE CONNECT ON DATABASE "+db+" FROM PUBLIC"); err != nil {
		return safePG(err)
	}
	if _, err := conn.Exec(ctx, "GRANT CONNECT ON DATABASE "+db+" TO "+who); err != nil {
		return safePG(err)
	}
	return nil
}

// sweepDisposable drops stale creating rows whose run is not executing, and
// rows already dropping. A ready row is left in place.
func (s *Server) sweepDisposable(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	rows, err := s.store.ListUndroppedDisposables(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for _, row := range rows {
		switch row.State {
		case store.DisposableReady:
			continue
		case store.DisposableDropping:
			s.releaseDisposable(ctx, row.RunID)
		case store.DisposableCreating:
			if now.Sub(row.CreatedAt) < disposableCreatingGrace {
				continue
			}
			exec, err := s.store.RunExecuting(ctx, row.RunID)
			if err != nil || exec {
				continue
			}
			s.releaseDisposable(ctx, row.RunID)
		default:
			if s.log != nil {
				s.log.Error("disposable state", "run_id", row.RunID)
			}
		}
	}
	return nil
}

// releaseDisposable moves ready or creating to dropping, then drops.
// Callers that already marked dropping still drop. A name that fails the
// regex is not interpolated.
func (s *Server) releaseDisposable(ctx context.Context, runID string) {
	if s.store == nil || runID == "" {
		return
	}
	s.disposableOp.Lock()
	defer s.disposableOp.Unlock()
	row, err := s.store.GetDisposable(ctx, runID)
	if err != nil || row.State == store.DisposableDropped {
		return
	}
	if row.State == store.DisposableReady || row.State == store.DisposableCreating {
		if err := s.store.MarkDisposable(ctx, runID, store.DisposableDropping); err != nil {
			if s.log != nil {
				s.log.Error("disposable dropping", "run_id", runID, "err", err)
			}
			return
		}
	}
	if err := s.dropDatabase(ctx, row); err != nil {
		if s.log != nil {
			s.log.Error("disposable drop", "run_id", runID, "err", err.Error())
		}
		return
	}
	if err := s.store.MarkDisposable(ctx, runID, store.DisposableDropped); err != nil {
		if s.log != nil {
			s.log.Error("disposable dropped", "run_id", runID, "err", err)
		}
		return
	}
	s.forgetPassword(runID)
}

func (s *Server) dropDatabase(ctx context.Context, row store.Disposable) error {
	adminURL, err := stripPoolQuery(s.disposableAdminURL())
	if err != nil {
		return err
	}
	dbName, roleName, err := postgresURLIdentity(adminURL)
	if err != nil {
		return err
	}
	if !dropAllowed(row.DatabaseName, row.RoleName, dbName, roleName) {
		return errors.New("server: disposable name refused")
	}
	if err := dropDatabaseConn(ctx, adminURL, row.DatabaseName, s.dropTimeout()); err != nil {
		if !dropBlocked(err) {
			return err
		}
		if err := terminateDatabase(ctx, adminURL, row.DatabaseName); err != nil {
			return err
		}
		if err := dropDatabaseConn(ctx, adminURL, row.DatabaseName, s.dropTimeout()); err != nil {
			return err
		}
	}
	return dropRole(ctx, adminURL, row.RoleName)
}

func dropDatabaseConn(ctx context.Context, adminURL, name string, timeout time.Duration) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return safePG(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	return dropDatabaseIfExists(ctx, conn, name, timeout)
}

func dropRole(ctx context.Context, adminURL, role string) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return safePG(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize()); err != nil {
		return safePG(err)
	}
	return nil
}

func (s *Server) dropTimeout() time.Duration {
	if s.disposableDropFor > 0 {
		return s.disposableDropFor
	}
	return disposableDropTimeout
}

func dropDatabaseIfExists(ctx context.Context, conn *pgx.Conn, name string, timeout time.Duration) error {
	ms := int(timeout / time.Millisecond)
	if ms < 1 {
		ms = 1
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET lock_timeout = %d", ms)); err != nil {
		return safePG(err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET statement_timeout = %d", ms)); err != nil {
		return safePG(err)
	}
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
		return safePG(err)
	}
	return nil
}

func terminateDatabase(ctx context.Context, adminURL, name string) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return safePG(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `
		SELECT pid FROM pg_stat_activity
		WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
	if err != nil {
		return safePG(err)
	}
	defer rows.Close()
	var pids []int32
	for rows.Next() {
		var pid int32
		if err := rows.Scan(&pid); err != nil {
			return safePG(err)
		}
		pids = append(pids, pid)
	}
	if err := rows.Err(); err != nil {
		return safePG(err)
	}
	for _, pid := range pids {
		if _, err := conn.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
			return safePG(err)
		}
	}
	return nil
}

func dropAllowed(database, role, adminDB, adminRole string) bool {
	if !storeName(database) || !storeName(role) {
		return false
	}
	if adminDB != "" && database == adminDB {
		return false
	}
	if adminRole != "" && role == adminRole {
		return false
	}
	return true
}

func storeName(name string) bool {
	const prefix = "factory_ev_"
	if len(name) != len(prefix)+32 || !strings.HasPrefix(name, prefix) {
		return false
	}
	for _, c := range name[len(prefix):] {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

type pgFailure struct {
	code string
}

func (e *pgFailure) Error() string {
	if e == nil || e.code == "" {
		return "server: disposable"
	}
	return "server: postgres " + e.code
}

func dropBlocked(err error) bool {
	code := pgCode(err)
	return code == "55P03" || code == "57014"
}

func safePG(err error) error {
	if err == nil {
		return nil
	}
	if privilegeDeniedPG(err) {
		return errDisposablePrivilege
	}
	var already *pgFailure
	if errors.As(err, &already) {
		return already
	}
	code := pgCode(err)
	if code == "" {
		return errors.New("server: disposable")
	}
	return &pgFailure{code: code}
}

func privilegeDeniedPG(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	var fail *pgFailure
	if errors.As(err, &fail) {
		return fail.code
	}
	return ""
}

func newDisposableName() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "factory_ev_" + hex.EncodeToString(raw[:]), nil
}

func newDisposablePassword() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Server) rememberPassword(runID, password string) {
	s.disposableMu.Lock()
	defer s.disposableMu.Unlock()
	if s.disposablePasswords == nil {
		s.disposablePasswords = map[string]string{}
	}
	s.disposablePasswords[runID] = password
}

func (s *Server) password(runID string) string {
	s.disposableMu.Lock()
	defer s.disposableMu.Unlock()
	if s.disposablePasswords == nil {
		return ""
	}
	return s.disposablePasswords[runID]
}

func (s *Server) forgetPassword(runID string) {
	s.disposableMu.Lock()
	defer s.disposableMu.Unlock()
	delete(s.disposablePasswords, runID)
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func postgresURLIdentity(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil || u.Host == "" {
		return "", "", errors.New("server: disposable admin url")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", "", errors.New("server: disposable admin url")
	}
	db := strings.TrimPrefix(u.Path, "/")
	role := u.User.Username()
	if db == "" || strings.Contains(db, "/") || role == "" {
		return "", "", errors.New("server: disposable admin url")
	}
	return db, role, nil
}

func taskDSN(adminURL, role, password, database string) (string, error) {
	u, err := url.Parse(adminURL)
	if err != nil || u.Host == "" {
		return "", errors.New("server: disposable admin url")
	}
	u.User = url.UserPassword(role, password)
	u.Path = "/" + database
	u.Fragment = ""
	ssl := u.Query().Get("sslmode")
	u.RawQuery = ""
	if ssl != "" {
		u.RawQuery = url.Values{"sslmode": []string{ssl}}.Encode()
	}
	return u.String(), nil
}

func stripPoolQuery(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Del("pool_max_conns")
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func swapDatabase(raw, database string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Path = "/" + database
	return u.String(), nil
}
