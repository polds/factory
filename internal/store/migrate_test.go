package store

import (
	"strings"
	"testing"
)

func TestSplitSQLKeepsDollarQuotedBody(t *testing.T) {
	t.Parallel()
	body := "SELECT 1;\n" +
		"CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$\n" +
		"BEGIN\n" +
		"  RAISE EXCEPTION 'a;b';\n" +
		"END;\n" +
		"$$;\n" +
		"SELECT 2;"
	got := splitSQL(body)
	if len(got) != 3 {
		t.Fatalf("statements %d: %#v", len(got), got)
	}
	if !strings.Contains(got[1], "RAISE EXCEPTION 'a;b'") {
		t.Fatalf("function split apart: %s", got[1])
	}
	if got[0] != "SELECT 1" || got[2] != "SELECT 2" {
		t.Fatalf("edges %#v", got)
	}
}

func TestSplitSQLMatchesLegacyMigrations(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"migrations/0001_init.sql", "migrations/0002_m3.sql"} {
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		got := splitSQL(string(body))
		legacy := legacySplit(string(body))
		if len(got) != len(legacy) {
			t.Fatalf("%s statements %d want %d", name, len(got), len(legacy))
		}
		for i := range got {
			if got[i] != legacy[i] {
				t.Fatalf("%s statement %d\n got: %s\nwant: %s", name, i, got[i], legacy[i])
			}
		}
	}
}

func legacySplit(body string) []string {
	parts := strings.Split(body, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		stmt := strings.TrimSpace(part)
		if stmt == "" {
			continue
		}
		out = append(out, stmt)
	}
	return out
}
