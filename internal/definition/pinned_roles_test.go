package definition

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoadPinnedRolesProjectsSelection(t *testing.T) {
	fix := newPinnedFixture(t)
	tree := fix.load(t, fourSlugs, "nils-aaberg")
	if !tree.Synthetic || tree.AtumRoster || tree.AllowAncestorShortcut || tree.Delegation != "direct_manager" {
		t.Fatalf("policy flags %+v", tree)
	}
	if tree.MaxDepth != NativeProofMaxDepth || tree.AbsoluteMaxDepth != NativeAbsoluteMaxDepth {
		t.Fatalf("depth %+v", tree)
	}
	if tree.SourceCommit != "source-commit" || tree.RosterSHA256 != fix.rosterSHA || tree.RosterGitBlob != fix.rosterBlob {
		t.Fatalf("provenance %+v", tree)
	}
	if len(tree.Personas) != 4 {
		t.Fatalf("personas %d", len(tree.Personas))
	}
	for _, banned := range []string{"ida", "quinn", "nour", "vera", "hugo"} {
		if persona(tree, banned).Name != "" {
			t.Fatalf("unselected seat %s", banned)
		}
	}
	root := persona(tree, "nils-aaberg")
	if root.ReportsTo != "" || root.SpawnableBy != nil || root.Role != "manager" || root.SourceReportsTo != "quinn" || root.SourceDottedTo != "" {
		t.Fatalf("root %+v", root)
	}
	if persona(tree, "beatriz-salgado").ReportsTo != "nils-aaberg" || persona(tree, "oskar-lindgren").ReportsTo != "nils-aaberg" || persona(tree, "viktor-halas").ReportsTo != "beatriz-salgado" {
		t.Fatal("projected edges")
	}
	if len(persona(tree, "beatriz-salgado").SpawnableBy) != 1 || persona(tree, "beatriz-salgado").SpawnableBy[0] != "nils-aaberg" {
		t.Fatal("spawnableBy")
	}
	checked, err := ValidateNativeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	if checked.Root != "nils-aaberg" || checked.Depth["nils-aaberg"] != 0 || checked.Depth["beatriz-salgado"] != 1 || checked.Depth["oskar-lindgren"] != 1 || checked.Depth["viktor-halas"] != 2 {
		t.Fatalf("depths %+v", checked.Depth)
	}
	witness, err := ValidateNativeTree(proofTree())
	if err != nil || checked.Digest == witness.Digest || checked.Digest == "" {
		t.Fatalf("digest %s witness %s", checked.Digest, witness.Digest)
	}
	again := fix.load(t, fourSlugs, "nils-aaberg")
	againChecked, err := ValidateNativeTree(again)
	if err != nil || againChecked.Digest != checked.Digest {
		t.Fatalf("stable %v", err)
	}
	for _, slug := range fourSlugs {
		seat := persona(tree, slug)
		if len(seat.Files) != 4 {
			t.Fatalf("%s files %d", slug, len(seat.Files))
		}
		raw, err := json.Marshal(seat)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("sourceDottedTo")) || bytes.Contains(raw, []byte("roster-only-marker")) {
			t.Fatalf("persona json leaked %s", slug)
		}
		prompt, err := NativeSeatPrompt(tree.Personas, slug)
		if err != nil {
			t.Fatal(err)
		}
		for _, other := range fourSlugs {
			if other != slug && bytes.Contains([]byte(prompt), []byte("SEAT:"+other)) {
				t.Fatalf("%s prompt contains %s", slug, other)
			}
		}
		for i, name := range pinnedRoleFiles {
			if seat.Files[i].Name != name {
				t.Fatalf("order %s", seat.Files[i].Name)
			}
			got := fileSection(t, prompt, name)
			if !bytes.Equal(got, seat.Files[i].Bytes) {
				t.Fatalf("%s %s bytes", slug, name)
			}
		}
	}
	legacy, err := NativeSeatPrompt(proofTree().Personas, "coordinator")
	if err != nil || legacy != "native activation" {
		t.Fatalf("legacy prompt %q %v", legacy, err)
	}
}

func TestLoadPinnedRolesExplicitFifth(t *testing.T) {
	fix := newPinnedFixture(t)
	four := fix.load(t, fourSlugs, "nils-aaberg")
	fiveSlugs := append(append([]string{}, fourSlugs...), "vera")
	tree := fix.load(t, fiveSlugs, "nils-aaberg")
	if len(tree.Personas) != 5 || persona(tree, "vera").ReportsTo != "beatriz-salgado" || persona(tree, "nour").Name != "" {
		t.Fatalf("fifth %+v", namesOf(tree))
	}
	checked, err := ValidateNativeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	fourChecked, err := ValidateNativeTree(four)
	if err != nil || checked.Digest == fourChecked.Digest {
		t.Fatal("explicit fifth did not change the digest")
	}
	if checked.Depth["vera"] != 2 {
		t.Fatalf("vera depth %d", checked.Depth["vera"])
	}
}

func TestLoadPinnedRolesByteChangesDigest(t *testing.T) {
	fix := newPinnedFixture(t)
	first := fix.load(t, fourSlugs, "nils-aaberg")
	firstChecked, err := ValidateNativeTree(first)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fix.root, "org", "agents", "nils-aaberg", "SOUL.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte{}, body...), 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fix.request(fourSlugs, "nils-aaberg").load(); err == nil || !errors.Is(err, ErrPinnedRoles) {
		t.Fatalf("old pin %v", err)
	}
	updated := fix
	updated.pins["nils-aaberg"]["SOUL.md"] = pinBytes(t, append(append([]byte{}, body...), 'x'))
	second := updated.load(t, fourSlugs, "nils-aaberg")
	secondChecked, err := ValidateNativeTree(second)
	if err != nil || secondChecked.Digest == firstChecked.Digest {
		t.Fatalf("updated digest %v", err)
	}
}

func TestLoadPinnedRolesRejects(t *testing.T) {
	fix := newPinnedFixture(t)
	base := fix.request(fourSlugs, "nils-aaberg")
	cases := []struct {
		name string
		edit func(t *testing.T, in *PinnedRoles)
	}{
		{"missing file", func(t *testing.T, in *PinnedRoles) {
			if err := os.Remove(filepath.Join(in.Root, "org", "agents", "viktor-halas", "TOOLS.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"dotdot slug", func(t *testing.T, in *PinnedRoles) {
			in.Slugs = append(append([]string{}, fourSlugs...), "../viktor-halas")
			in.ProofRoot = "nils-aaberg"
		}},
		{"absolute slug", func(t *testing.T, in *PinnedRoles) {
			in.Slugs = []string{"/tmp/nils-aaberg", "beatriz-salgado", "oskar-lindgren", "viktor-halas"}
		}},
		{"duplicate slug", func(t *testing.T, in *PinnedRoles) {
			in.Slugs = []string{"nils-aaberg", "nils-aaberg", "beatriz-salgado", "oskar-lindgren"}
		}},
		{"unknown slug", func(t *testing.T, in *PinnedRoles) {
			in.Slugs = append(append([]string{}, fourSlugs...), "not-a-seat")
			in.Files["not-a-seat"] = in.Files["vera"]
		}},
		{"outside manager as fifth", func(t *testing.T, in *PinnedRoles) {
			in.Slugs = append(append([]string{}, fourSlugs...), "quinn")
			in.Files["quinn"] = in.Files["nils-aaberg"]
		}},
		{"proof root is not outside", func(t *testing.T, in *PinnedRoles) {
			in.ProofRoot = "beatriz-salgado"
		}},
		{"empty selection", func(t *testing.T, in *PinnedRoles) {
			in.Slugs = nil
		}},
		{"proof root omitted", func(t *testing.T, in *PinnedRoles) {
			in.ProofRoot = "vera"
		}},
		{"roster sha", func(t *testing.T, in *PinnedRoles) {
			in.RosterSHA256 = strings.Repeat("0", 64)
		}},
		{"roster git blob", func(t *testing.T, in *PinnedRoles) {
			in.RosterGitBlob = strings.Repeat("ab", 20)
		}},
		{"file size", func(t *testing.T, in *PinnedRoles) {
			pin := in.Files["nils-aaberg"]["AGENTS.md"]
			pin.Size++
			in.Files["nils-aaberg"]["AGENTS.md"] = pin
		}},
		{"file sha", func(t *testing.T, in *PinnedRoles) {
			pin := in.Files["oskar-lindgren"]["HEARTBEAT.md"]
			pin.SHA256 = strings.Repeat("0", 64)
			in.Files["oskar-lindgren"]["HEARTBEAT.md"] = pin
		}},
		{"file git blob", func(t *testing.T, in *PinnedRoles) {
			pin := in.Files["beatriz-salgado"]["SOUL.md"]
			pin.GitBlob = strings.Repeat("cd", 20)
			in.Files["beatriz-salgado"]["SOUL.md"] = pin
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base.clone()
			tc.edit(t, &in)
			if _, err := in.load(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestLoadPinnedRolesRejectsPathsAndRoster(t *testing.T) {
	fix := newPinnedFixture(t)
	t.Run("symlink file", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "real")
		copyDir(t, fix.root, root)
		soul := filepath.Join(root, "org", "agents", "nils-aaberg", "SOUL.md")
		if err := os.Rename(soul, soul+".real"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(soul+".real", soul); err != nil {
			t.Fatal(err)
		}
		in := fix.request(fourSlugs, "nils-aaberg")
		in.Root = root
		if _, err := in.load(); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink file %v", err)
		}
	})
	t.Run("symlink root", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(fix.root, link); err != nil {
			t.Fatal(err)
		}
		in := fix.request(fourSlugs, "nils-aaberg")
		in.Root = link
		if _, err := in.load(); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink root %v", err)
		}
	})
	t.Run("symlink escape", func(t *testing.T) {
		outer := t.TempDir()
		root := filepath.Join(outer, "root")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(fix.root, "org"), filepath.Join(root, "org")); err != nil {
			t.Fatal(err)
		}
		in := fix.request(fourSlugs, "nils-aaberg")
		in.Root = root
		if _, err := in.load(); err == nil {
			t.Fatal("escaped through a directory symlink")
		}
	})
	t.Run("over cap file", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "cap")
		copyDir(t, fix.root, root)
		big := bytes.Repeat([]byte("a"), pinnedRoleFileCap+1)
		if err := os.WriteFile(filepath.Join(root, "org", "agents", "nils-aaberg", "AGENTS.md"), big, 0o644); err != nil {
			t.Fatal(err)
		}
		in := fix.request(fourSlugs, "nils-aaberg")
		in.Root = root
		if _, err := in.load(); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("cap %v", err)
		}
	})
	t.Run("over cap roster", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "roster-cap")
		if err := os.MkdirAll(filepath.Join(root, "org"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "org", "roster.yaml"), bytes.Repeat([]byte("a"), pinnedRosterCap+1), 0o644); err != nil {
			t.Fatal(err)
		}
		in := fix.request(fourSlugs, "nils-aaberg")
		in.Root = root
		if _, err := in.load(); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("roster cap %v", err)
		}
	})
	t.Run("missing root", func(t *testing.T) {
		in := fix.request(fourSlugs, "nils-aaberg")
		in.Root = filepath.Join(t.TempDir(), "missing")
		if _, err := in.load(); err == nil {
			t.Fatal("missing root")
		}
	})
	t.Run("root file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		in := fix.request(fourSlugs, "nils-aaberg")
		in.Root = file
		if _, err := in.load(); err == nil {
			t.Fatal("file root")
		}
	})
	t.Run("duplicate roster slug", func(t *testing.T) {
		rejectRoster(t, fix, rosterYAML+"  - slug: nils-aaberg\n    role: manager\n    reports_to: quinn\n")
	})
	t.Run("unknown role", func(t *testing.T) {
		rejectRoster(t, fix, strings.Replace(rosterYAML, "role: worker\n    reports_to: nils-aaberg\n    dotted_to: beatriz-salgado\n", "role: intern\n    reports_to: nils-aaberg\n", 1))
	})
	t.Run("unknown manager", func(t *testing.T) {
		rejectRoster(t, fix, strings.Replace(rosterYAML, "reports_to: quinn\n", "reports_to: nobody\n", 1))
	})
	t.Run("cycle", func(t *testing.T) {
		rejectRoster(t, fix, rosterYAML+"  - slug: left\n    role: worker\n    reports_to: right\n  - slug: right\n    role: worker\n    reports_to: left\n")
	})
	t.Run("dotted selected", func(t *testing.T) {
		rejectRoster(t, fix, strings.Replace(rosterYAML, "  - slug: viktor-halas\n    role: general\n    reports_to: beatriz-salgado\n", "  - slug: viktor-halas\n    role: general\n    reports_to: beatriz-salgado\n    dotted_to: oskar-lindgren\n", 1))
	})
	t.Run("general parent", func(t *testing.T) {
		root := writePinnedRoot(t, rosterYAML+"  - slug: pip\n    role: general\n    reports_to: oskar-lindgren\n", append(append([]string{}, fourSlugs...), "pip"))
		pins := readPins(t, root, append(append([]string{}, fourSlugs...), "pip"))
		_, err := LoadPinnedRoles(PinnedRoles{
			Root: root, SourceCommit: "source-commit", RosterSHA256: hashFile(t, filepath.Join(root, "org", "roster.yaml")),
			RosterGitBlob: gitBlobID(mustRead(t, filepath.Join(root, "org", "roster.yaml"))),
			Slugs:         append(append([]string{}, fourSlugs...), "pip"), ProofRoot: "nils-aaberg", Files: pins,
		})
		if err == nil || !strings.Contains(err.Error(), "projected parent") {
			t.Fatalf("general parent %v", err)
		}
	})
	t.Run("roster shape", func(t *testing.T) {
		root := writePinnedRoot(t, "agents: []\n", nil)
		in := fix.request(fourSlugs, "nils-aaberg")
		in.Root = root
		body := mustRead(t, filepath.Join(root, "org", "roster.yaml"))
		in.RosterSHA256 = hex.EncodeToString(sum(body))
		in.RosterGitBlob = gitBlobID(body)
		if _, err := in.load(); err == nil {
			t.Fatal("shape")
		}
	})
}

func TestNativeSeatPromptShape(t *testing.T) {
	files := []RoleFile{
		{Name: "AGENTS.md", Bytes: []byte("alpha")},
		{Name: "HEARTBEAT.md", Bytes: []byte("beta\n")},
		{Name: "SOUL.md", Bytes: []byte("gamma")},
		{Name: "TOOLS.md", Bytes: []byte("delta")},
	}
	got, err := NativeSeatPrompt([]NativePersona{{Name: "nils", Files: files}}, "nils")
	if err != nil {
		t.Fatal(err)
	}
	want := "# Seat\nnils\n# File AGENTS.md\nbytes: 5\nalpha# File HEARTBEAT.md\nbytes: 5\nbeta\n# File SOUL.md\nbytes: 5\ngamma# File TOOLS.md\nbytes: 5\ndelta"
	if got != want {
		t.Fatalf("prompt %q", got)
	}
	short := append([]RoleFile{}, files[:3]...)
	if _, err := NativeSeatPrompt([]NativePersona{{Name: "nils", Files: files}, {Name: "child", Files: short}}, "child"); err == nil {
		t.Fatal("short seat")
	}
	swapped := append([]RoleFile{}, files...)
	swapped[0], swapped[1] = swapped[1], swapped[0]
	if _, err := NativeSeatPrompt([]NativePersona{{Name: "nils", Files: swapped}}, "nils"); err == nil {
		t.Fatal("order")
	}
}

func TestLoadPinnedRolesReferenceSnapshot(t *testing.T) {
	root := os.Getenv("PINNED_ROLE_ROOT")
	if root == "" {
		t.Skip("PINNED_ROLE_ROOT unset")
	}
	files := map[string]map[string]PinnedFile{}
	for slug, pins := range referencePins {
		files[slug] = map[string]PinnedFile{}
		for name, pin := range pins {
			files[slug][name] = pin
		}
	}
	tree, err := LoadPinnedRoles(PinnedRoles{
		Root: root, SourceCommit: referenceCommit, RosterSHA256: referenceRosterSHA, RosterGitBlob: referenceRosterBlob,
		Slugs: fourSlugs, ProofRoot: "nils-aaberg", Files: files,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Personas) != 4 || tree.SourceCommit != referenceCommit || tree.RosterSHA256 != referenceRosterSHA {
		t.Fatalf("snapshot seats %d", len(tree.Personas))
	}
	rootSeat := persona(tree, "nils-aaberg")
	if rootSeat.ReportsTo != "" || rootSeat.SourceReportsTo != "tobias-lindqvist" || rootSeat.Role != "manager" || rootSeat.SourceDottedTo != "" {
		t.Fatal("reference root edges")
	}
	checked, err := ValidateNativeTree(tree)
	if err != nil || checked.Depth["viktor-halas"] != 2 || checked.Depth["oskar-lindgren"] != 1 {
		t.Fatalf("reference depths %+v %v", checked.Depth, err)
	}
	for _, slug := range fourSlugs {
		prompt, err := NativeSeatPrompt(tree.Personas, slug)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range pinnedRoleFiles {
			got := fileSection(t, prompt, name)
			sum := sha256.Sum256(got)
			pin := files[slug][name]
			if hex.EncodeToString(sum[:]) != pin.SHA256 || len(got) != pin.Size {
				t.Fatalf("%s %s hash or size", slug, name)
			}
		}
	}
}

const rosterYAML = `meta:
  org: synthetic
  note: roster-only-marker
agents:
  - slug: ida
    role: ceo
  - slug: quinn
    role: cto
    reports_to: ida
  - slug: nils-aaberg
    role: manager
    reports_to: quinn
    interval_s: 14400
    model:
      id: ignored-model
  - slug: beatriz-salgado
    role: manager
    reports_to: nils-aaberg
  - slug: oskar-lindgren
    role: general
    reports_to: nils-aaberg
  - slug: viktor-halas
    role: general
    reports_to: beatriz-salgado
  - slug: nour
    role: general
    reports_to: nils-aaberg
  - slug: vera
    role: general
    reports_to: beatriz-salgado
  - slug: hugo
    role: worker
    reports_to: nils-aaberg
    dotted_to: beatriz-salgado
`

var fourSlugs = []string{"nils-aaberg", "beatriz-salgado", "oskar-lindgren", "viktor-halas"}

type pinnedFixture struct {
	root       string
	pins       map[string]map[string]PinnedFile
	rosterSHA  string
	rosterBlob string
}

func newPinnedFixture(t *testing.T) pinnedFixture {
	t.Helper()
	root := writePinnedRoot(t, rosterYAML, append(append([]string{}, fourSlugs...), "vera"))
	body := mustRead(t, filepath.Join(root, "org", "roster.yaml"))
	return pinnedFixture{
		root: root, pins: readPins(t, root, append(append([]string{}, fourSlugs...), "vera")),
		rosterSHA: hex.EncodeToString(sum(body)), rosterBlob: gitBlobID(body),
	}
}

func (f pinnedFixture) request(slugs []string, proof string) PinnedRoles {
	files := map[string]map[string]PinnedFile{}
	for _, slug := range slugs {
		if pins, ok := f.pins[slug]; ok {
			files[slug] = clonePins(pins)
		}
	}
	return PinnedRoles{
		Root: f.root, SourceCommit: "source-commit", RosterSHA256: f.rosterSHA, RosterGitBlob: f.rosterBlob,
		Slugs: append([]string{}, slugs...), ProofRoot: proof, Files: files,
	}
}

func (f pinnedFixture) load(t *testing.T, slugs []string, proof string) NativeTree {
	t.Helper()
	tree, err := f.request(slugs, proof).load()
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func (in PinnedRoles) load() (NativeTree, error) {
	return LoadPinnedRoles(in)
}

func (in PinnedRoles) clone() PinnedRoles {
	out := in
	out.Slugs = append([]string{}, in.Slugs...)
	out.Files = map[string]map[string]PinnedFile{}
	for slug, pins := range in.Files {
		out.Files[slug] = clonePins(pins)
	}
	return out
}

func clonePins(in map[string]PinnedFile) map[string]PinnedFile {
	out := map[string]PinnedFile{}
	for name, pin := range in {
		out[name] = pin
	}
	return out
}

func writePinnedRoot(t *testing.T, roster string, seats []string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "org"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "org", "roster.yaml"), []byte(roster), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, slug := range seats {
		dir := filepath.Join(root, "org", "agents", slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range pinnedRoleFiles {
			body := []byte("SEAT:" + slug + "\nFILE:" + name + "\n")
			if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func readPins(t *testing.T, root string, seats []string) map[string]map[string]PinnedFile {
	t.Helper()
	out := map[string]map[string]PinnedFile{}
	for _, slug := range seats {
		out[slug] = map[string]PinnedFile{}
		for _, name := range pinnedRoleFiles {
			out[slug][name] = pinBytes(t, mustRead(t, filepath.Join(root, "org", "agents", slug, name)))
		}
	}
	return out
}

func pinBytes(t *testing.T, body []byte) PinnedFile {
	t.Helper()
	return PinnedFile{SHA256: hex.EncodeToString(sum(body)), GitBlob: gitBlobID(body), Size: len(body)}
}

func rejectRoster(t *testing.T, fix pinnedFixture, roster string) {
	t.Helper()
	root := writePinnedRoot(t, roster, fourSlugs)
	body := mustRead(t, filepath.Join(root, "org", "roster.yaml"))
	in := fix.request(fourSlugs, "nils-aaberg")
	in.Root = root
	in.RosterSHA256 = hex.EncodeToString(sum(body))
	in.RosterGitBlob = gitBlobID(body)
	in.Files = readPins(t, root, fourSlugs)
	if _, err := in.load(); err == nil {
		t.Fatal("accepted")
	}
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	return hex.EncodeToString(sum(mustRead(t, path)))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func sum(body []byte) []byte {
	sum := sha256.Sum256(body)
	return sum[:]
}

func namesOf(tree NativeTree) []string {
	var names []string
	for _, persona := range tree.Personas {
		names = append(names, persona.Name)
	}
	return names
}

func fileSection(t *testing.T, prompt, name string) []byte {
	t.Helper()
	header := "# File " + name + "\nbytes: "
	i := strings.Index(prompt, header)
	if i < 0 {
		t.Fatalf("missing %s", name)
	}
	rest := prompt[i+len(header):]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		t.Fatal("length")
	}
	n, err := strconv.Atoi(rest[:nl])
	if err != nil {
		t.Fatal(err)
	}
	body := rest[nl+1:]
	if len(body) < n {
		t.Fatalf("short %s", name)
	}
	return []byte(body[:n])
}

const referenceCommit = "a18fbb6b0e29442872f09e704be996208d0e6e8d"
const referenceRosterSHA = "01b8d5ec72829b82760aeae0f4632f34a1cb32101ec4c1a43e0ed2f9d99af924"
const referenceRosterBlob = "5312fed1e3a7f708a7e66edfa380be602d2e8951"

var referencePins = map[string]map[string]PinnedFile{
	"nils-aaberg": {
		"AGENTS.md":    {SHA256: "001c3e5f750383a7676d1de6e45c959fef1a21a9742eb9d9c0f11b377b1ec8f9", GitBlob: "627a7b6411d99793cf50206e1116afb81d6080cb", Size: 4378},
		"HEARTBEAT.md": {SHA256: "793919caeef4b6f4f48e9c33e85a8a585e9cf9456ba3451c76bffafea0f5ec8a", GitBlob: "8c328f6c22f6a154b168bab7e5d999bfe1513d3b", Size: 2210},
		"SOUL.md":      {SHA256: "4a30c61a448ac94f00399bc225f3b3d04acf576d4576ff6212c2628076e39970", GitBlob: "2b36920eef817c6f1fe70ebb37508a0613990a13", Size: 1201},
		"TOOLS.md":     {SHA256: "cc252e86d8a41c261380a703a5f4d823ec01d9644d74cb3af314f82013878b46", GitBlob: "22e88566c089b1b99688a6e79dcedde2e9685f23", Size: 1298},
	},
	"beatriz-salgado": {
		"AGENTS.md":    {SHA256: "d09e94785bc71dd1ffda7b0cb05c087c73ac81bdfbfbf73edf460d7a6e7c1554", GitBlob: "133a1d15c5aa51e29c324d456ad8f75e3bdf2796", Size: 2644},
		"HEARTBEAT.md": {SHA256: "0af47628326dc6d50be45c704d8a56ff78698434c909692c95646d80d3028b86", GitBlob: "7c3f0e38759764e0c369807f9bae7d30e926f962", Size: 2154},
		"SOUL.md":      {SHA256: "8bdd01478c9ca0fb11f8e34e2eddefc0484713b38642f72aa3bc7122f6279ad6", GitBlob: "d298b74311ba1396471f46b02be4a689ca91cfec", Size: 1137},
		"TOOLS.md":     {SHA256: "9679d750b5104aec240023215d66a5f1a8d4bd43a5f3459b3cd2976e46218968", GitBlob: "b44d82db304e2c09e6d059156b5c93da067d224d", Size: 1258},
	},
	"viktor-halas": {
		"AGENTS.md":    {SHA256: "72e1469593b9b1da47339ae303a626a4c865cdce3438fda2f9c3639ed890b430", GitBlob: "019133053c3decfb7455443d9ed0c3a9fc42c91a", Size: 2222},
		"HEARTBEAT.md": {SHA256: "ed0e56bb3bbf8f1d8e39d5617343cf0d563dd22f5b4af66494ce17b67200d902", GitBlob: "d24f0eea33a5907b0a70d853955e79c2b65718e7", Size: 1853},
		"SOUL.md":      {SHA256: "cf795555d445c8482e869d715dc82acdaba3416d59e8faa2387b0655bfdd925c", GitBlob: "17e97bf635b79a3b9e9eb900fe5010e921364c1d", Size: 1178},
		"TOOLS.md":     {SHA256: "8793df55990b9ad6f732a58524ba27d7aacbb06155dad75c0bd9d3f6241cd97a", GitBlob: "c48f2f62fb803cc026b01be51412a15b3d74d4d0", Size: 1257},
	},
	"oskar-lindgren": {
		"AGENTS.md":    {SHA256: "12378fb6197a97ea9946e6679b951bb49b5c4d02c971c3e7d6c3b214f541876c", GitBlob: "b3f28e9cef1207e5700d1d5ea794f465507d4cf5", Size: 2271},
		"HEARTBEAT.md": {SHA256: "05870f775a380256e2306d95af964a4afc55ad249e971c410a92a63032d76ef6", GitBlob: "e667eaeac542ac1215af0871cc6202d91311f89f", Size: 1914},
		"SOUL.md":      {SHA256: "e22b567357c9b2145fce0f55227c91f29c8944ccb9739927aa04b27a2712aa31", GitBlob: "31de4403260d3f873bc799ed07ee3d6e9c1fc262", Size: 1237},
		"TOOLS.md":     {SHA256: "0e91e0f21f1a2b513d3c6b8d85521c7b110dd5debb93df9f4a43add5843050e2", GitBlob: "e02b0d8f15f09b6f7d9034a821f5e0816a5afa45", Size: 1399},
	},
}
