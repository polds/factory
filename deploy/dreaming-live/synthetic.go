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

	yaml "go.yaml.in/yaml/v3"

	"github.com/polds/factory/internal/definition"
)

// SeatFile is the committed seat list. The driver reads it; the bytes of
// the role files stay in the Dreaming checkout.
type SeatFile struct {
	ProofRoot       string   `yaml:"proof_root"`
	SourceReportsTo string   `yaml:"source_reports_to"`
	Implementer     string   `yaml:"implementer"`
	Reviewer        string   `yaml:"reviewer"`
	Roster          string   `yaml:"roster"`
	Files           []string `yaml:"files"`
	Seats           []string `yaml:"seats"`
	Purpose         string   `yaml:"purpose"`
	PublishRepo     string   `yaml:"publish_repo"`
	Model           string   `yaml:"model"`
}

// LoadSeats reads spike/dreaming/definition/seats.yaml.
func LoadSeats() (SeatFile, error) {
	raw, err := os.ReadFile(filepath.Join(mustModuleRoot(), "spike", "dreaming", "definition", "seats.yaml"))
	if err != nil {
		return SeatFile{}, err
	}
	var doc SeatFile
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return SeatFile{}, err
	}
	if doc.Purpose == "" {
		doc.Purpose = "work"
	}
	return doc, nil
}

// LoadGoal reads spike/dreaming/definition/goal-454.md.
func LoadGoal() (string, error) {
	raw, err := os.ReadFile(filepath.Join(mustModuleRoot(), "spike", "dreaming", "definition", "goal-454.md"))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(raw))
	if !strings.Contains(text, "factoryEvidencePass") || !strings.Contains(text, "FACTORY_EVIDENCE_MUTATION") {
		return "", fmt.Errorf("goal template")
	}
	return text, nil
}

// LoadTree loads a committed role checkout. Blob ids are computed from the files.
func LoadTree(dir string) (definition.NativeTree, string, map[string]string, error) {
	pin, err := gitOut(dir, "rev-parse", "HEAD")
	if err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	seats, err := LoadSeats()
	if err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	rosterRel := seats.Roster
	if rosterRel == "" {
		rosterRel = "org/roster.yaml"
	}
	files := seats.Files
	if len(files) == 0 {
		files = RoleFileNames
	}
	slugs := seats.Seats
	if len(slugs) == 0 {
		slugs = SeatSlugs
	}
	blobs := map[string]string{}
	bodies := map[string][]byte{}
	read := func(rel string) ([]byte, error) {
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		bodies[rel] = body
		blobs[rel] = gitBlobID(body)
		return body, nil
	}
	if _, err := read(rosterRel); err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	pins := map[string]map[string]definition.PinnedFile{}
	for _, slug := range slugs {
		pins[slug] = map[string]definition.PinnedFile{}
		for _, name := range files {
			rel := "org/agents/" + slug + "/" + name
			body, err := read(rel)
			if err != nil {
				return definition.NativeTree{}, "", nil, err
			}
			sum := sha256.Sum256(body)
			pins[slug][name] = definition.PinnedFile{
				SHA256: hex.EncodeToString(sum[:]), GitBlob: blobs[rel], Size: len(body),
			}
		}
	}
	roster := bodies[rosterRel]
	sum := sha256.Sum256(roster)
	root := seats.ProofRoot
	if root == "" {
		root = "nils-aaberg"
	}
	tree, err := definition.LoadPinnedRoles(definition.PinnedRoles{
		Root: dir, SourceCommit: pin,
		RosterSHA256: hex.EncodeToString(sum[:]), RosterGitBlob: blobs[rosterRel],
		Slugs: slugs, ProofRoot: root, Files: pins,
	})
	if err != nil {
		return definition.NativeTree{}, "", nil, err
	}
	return tree, pin, blobs, nil
}

func mustModuleRoot() string {
	root, err := moduleRoot()
	if err != nil {
		return "."
	}
	return root
}

const syntheticRoster = `agents:
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
// not role bytes. The pin includes Viktor's sentinel file for the rehearsal.
func BuildSynthetic(dir string) (definition.NativeTree, string, map[string]string, error) {
	return buildCheckout(dir, true)
}

// BuildLivePin is the real-pin shape: the package and its database test
// harness are committed, and Viktor's output file is not.
func BuildLivePin(dir string) (definition.NativeTree, string, map[string]string, error) {
	return buildCheckout(dir, false)
}

func buildCheckout(dir string, sentinel bool) (definition.NativeTree, string, map[string]string, error) {
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
	if err := writePinHarness(dir, sentinel); err != nil {
		return definition.NativeTree{}, "", nil, err
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
	if err := git(dir, "add", "org", "oracle", "go.mod"); err != nil {
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

func writePinHarness(dir string, sentinel bool) error {
	evidence := filepath.Join(dir, "oracle")
	if err := os.MkdirAll(evidence, 0o755); err != nil {
		return err
	}
	wiring := "package oracle\n\nimport \"database/sql\"\n\n// Open is the pin's database test harness. Viktor's tenant test is not here yet.\nfunc Open(dsn string) (*sql.DB, error) {\n\treturn sql.Open(\"postgres\", dsn)\n}\n"
	if err := os.WriteFile(filepath.Join(evidence, "db.go"), []byte(wiring), 0o644); err != nil {
		return err
	}
	mod := "module example.com/dreaming\n\ngo 1.26.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		return err
	}
	if !sentinel {
		return nil
	}
	body := "package oracle\n\nimport \"testing\"\n\nfunc TestTenantIsolation(t *testing.T) {\n\tif tenant() != \"factoryEvidencePass\" {\n\t\tt.Fatal(\"tenant\")\n\t}\n}\n\nfunc tenant() string { return \"factoryEvidencePass\" }\n"
	return os.WriteFile(filepath.Join(evidence, "tenant_test.go"), []byte(body), 0o644)
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
