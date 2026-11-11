package dreaminglive

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/polds/factory/internal/definition"
)

const syntheticRoster = `meta:
  org: synthetic
  note: roster-only-marker
agents:
  - slug: tobias-lindqvist
    role: ceo
  - slug: nils-aaberg
    role: manager
    reports_to: tobias-lindqvist
  - slug: beatriz-salgado
    role: manager
    reports_to: nils-aaberg
  - slug: viktor-halas
    role: general
    reports_to: beatriz-salgado
  - slug: oskar-lindgren
    role: general
    reports_to: nils-aaberg
`

// SeatSlugs is the frozen four-seat selection. The outside manager
// tobias-lindqvist stays in the roster and is not a seat.
var SeatSlugs = []string{"nils-aaberg", "beatriz-salgado", "viktor-halas", "oskar-lindgren"}

// RoleFileNames are the four pinned files for each seat.
var RoleFileNames = []string{"AGENTS.md", "HEARTBEAT.md", "SOUL.md", "TOOLS.md"}

// BuildSynthetic writes a temp Dreaming-shaped tree with synthetic seat
// text, commits it, and loads the frozen policy. File bodies are markers,
// not role bytes.
func BuildSynthetic(dir string) (definition.NativeTree, string, map[string]string, error) {
	if err := os.MkdirAll(filepath.Join(dir, "org", "agents"), 0o755); err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	rosterPath := filepath.Join(dir, "org", "roster.yaml")
	if err := os.WriteFile(rosterPath, []byte(syntheticRoster), 0o644); err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	bodies := map[string][]byte{"org/roster.yaml": []byte(syntheticRoster)}
	for _, slug := range SeatSlugs {
		seatDir := filepath.Join(dir, "org", "agents", slug)
		if err := os.MkdirAll(seatDir, 0o755); err != nil {
			return definition.NativeTree{}, "", nil, err
		}
		for _, name := range RoleFileNames {
			rel := "org/agents/" + slug + "/" + name
			body := []byte("synthetic-seat-marker\n" + slug + "\n" + name + "\n")
			if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), body, 0o644); err != nil {
				return definition.NativeTree{}, "", nil, err
			}
			bodies[rel] = body
		}
	}
	if err := git(dir, "init", "-b", "main"); err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	if err := git(dir, "config", "user.email", "dreaming@example.com"); err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	if err := git(dir, "config", "user.name", "dreaming"); err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	if err := git(dir, "add", "org"); err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	if err := git(dir, "commit", "-m", "synthetic seats"); err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	pin, err := gitOut(dir, "rev-parse", "HEAD")
	if err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	blobs := map[string]string{}
	pins := map[string]map[string]definition.PinnedFile{}
	for rel, body := range bodies {
		sum := sha256.Sum256(body)
		id := gitBlobID(body)
		blobs[rel] = id
		if rel == "org/roster.yaml" {
			continue
		}
		slug := strings.TrimPrefix(rel, "org/agents/")
		seat, name, _ := strings.Cut(slug, "/")
		if pins[seat] == nil {
			pins[seat] = map[string]definition.PinnedFile{}
		}
		pins[seat][name] = definition.PinnedFile{
			SHA256: hex.EncodeToString(sum[:]), GitBlob: id, Size: len(body),
		}
	}
	rosterSum := sha256.Sum256(bodies["org/roster.yaml"])
	tree, err := definition.LoadPinnedRoles(definition.PinnedRoles{
		Root:          dir,
		SourceCommit:  pin,
		RosterSHA256:  hex.EncodeToString(rosterSum[:]),
		RosterGitBlob: blobs["org/roster.yaml"],
		Slugs:         append([]string{}, SeatSlugs...),
		ProofRoot:     "nils-aaberg",
		Files:         pins,
	})
	if err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	return tree, pin, blobs, nil
}

func gitBlobID(body []byte) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(body))
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func git(dir string, args ...string) error {
	_, err := gitOut(dir, args...)
	return err
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_EMAIL=dreaming@example.com",
		"GIT_AUTHOR_NAME=dreaming",
		"GIT_COMMITTER_EMAIL=dreaming@example.com",
		"GIT_COMMITTER_NAME=dreaming",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if len(text) > 200 {
			text = text[:200]
		}
		return "", fmt.Errorf("git %s: %s", args[0], text)
	}
	return strings.TrimSpace(string(out)), nil
}
