package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnvFlag(t *testing.T) {
	var e envFlag
	if err := e.Set("A=1"); err != nil {
		t.Fatal(err)
	}
	if err := e.Set("B=x=y"); err != nil {
		t.Fatal(err)
	}
	if err := e.Set("bad"); err == nil {
		t.Fatal("want error for missing =")
	}
	if e.m["A"] != "1" || e.m["B"] != "x=y" {
		t.Fatalf("got %v", e.m)
	}
}

func TestEnvFlagLaterWinsAndStringOmitsValues(t *testing.T) {
	var e envFlag
	const secret = "super-secret-token"
	if err := e.Set("TOKEN=first"); err != nil {
		t.Fatal(err)
	}
	if err := e.Set("TOKEN=" + secret); err != nil {
		t.Fatal(err)
	}
	if e.m["TOKEN"] != secret {
		t.Fatalf("later -env should win, got %q", e.m["TOKEN"])
	}
	if strings.Contains(e.String(), secret) {
		t.Fatalf("String leaked value: %q", e.String())
	}
	if !strings.Contains(e.String(), "TOKEN") {
		t.Fatalf("String = %q, want key", e.String())
	}
}

func TestLoadEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "e.env")
	if err := os.WriteFile(p, []byte("# c\n\nA=1\nA=2\nB=3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := loadEnvFile(p)
	if err != nil || m["A"] != "2" || m["B"] != "3" || len(m) != 2 {
		t.Fatalf("%v %v", m, err)
	}
}

func TestLoadEnvFileRejectsBadLineWithoutLeaking(t *testing.T) {
	p := filepath.Join(t.TempDir(), "e.env")
	const leaked = "super-secret-token"
	if err := os.WriteFile(p, []byte("  # comment\n\n"+leaked+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadEnvFile(p)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), leaked) {
		t.Fatalf("error leaked line: %v", err)
	}
}

func TestMergeEnvFlagBeatsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "e.env")
	if err := os.WriteFile(p, []byte("# c\nA=from-file\nB=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := mergeEnv(p, map[string]string{"A": "from-flag", "C": "from-flag"})
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "from-flag" || m["B"] != "from-file" || m["C"] != "from-flag" || len(m) != 3 {
		t.Fatalf("%v", m)
	}
}

func TestBuildAssignmentDefaultsAndEnv(t *testing.T) {
	now := time.Date(2026, 10, 13, 3, 20, 59, 0, time.UTC)
	a := buildAssignment(now, assignOpts{
		Prefix: "run_m0_", Title: "M0 protocol spike", Prompt: "p",
		Model: "m0-stub-model", DockerImage: "alpine:3.20", SidecarImage: "s",
		Env: map[string]string{"X": "y"},
	})
	if a.TaskID != "run_m0_20261013T032059" {
		t.Fatal(a.TaskID)
	}
	if a.EnvVars["FACTORY_M0_MARKER"] != a.TaskID || a.EnvVars["X"] != "y" || a.EnvVars["FACTORY_TASK_PROMPT"] != "p" {
		t.Fatal(a.EnvVars)
	}
	if *a.Task.AgentConfigSnapshot.ModelID != "m0-stub-model" {
		t.Fatal("model")
	}
}
