package definition

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	yaml "go.yaml.in/yaml/v3"
)

const (
	pinnedRoleFileCap = 65536
	pinnedRosterCap   = 262144
	rosterRel         = "org/roster.yaml"
	// BundleModeRoleFiles is the explicit seat-bundle envelope mode.
	BundleModeRoleFiles = "role_files"
	// NativeGoalBodyCap is the maximum raw title and instruction body in a
	// bundled goal section. Longer input fails. The bytes are not trimmed.
	NativeGoalBodyCap = 65536
)

// pinnedRoleFiles is the historical seat list. A declaration replaces it.
var pinnedRoleFiles = []string{"AGENTS.md", "HEARTBEAT.md", "SOUL.md", "TOOLS.md"}

// RoleLayout is the role-file declaration applied to one load or attest.
// A zero layout is not a declaration. Callers that omit it keep the
// historical org layout so an already-built bundle still loads.
type RoleLayout struct {
	RosterPath       string
	PathTemplate     string
	Files            []string
	MaxDepth         int
	AbsoluteMaxDepth int
	Root             string
	Implementer      string
	Reviewer         string
}

// Declared reports whether the layout names a roster or a seat template.
func (l RoleLayout) Declared() bool {
	return l.RosterPath != "" || l.PathTemplate != "" || len(l.Files) > 0
}

// HistoricalRoleLayout is the layout used when a caller does not pass one.
func HistoricalRoleLayout() RoleLayout {
	return RoleLayout{
		RosterPath:       rosterRel,
		PathTemplate:     "org/agents/{slug}/{file}",
		Files:            append([]string{}, pinnedRoleFiles...),
		MaxDepth:         NativeProofMaxDepth,
		AbsoluteMaxDepth: NativeAbsoluteMaxDepth,
	}
}

// ErrPinnedRoles means the pinned snapshot was not loaded.
var ErrPinnedRoles = errors.New("pinned roles")

// PinnedFile is the caller's content pin for one role file.
type PinnedFile struct {
	SHA256  string
	GitBlob string
	Size    int
}

// PinnedRoles is an explicit snapshot selection. Slugs are the only seats
// loaded. The loader does not add a seat the caller left out.
type PinnedRoles struct {
	Root          string
	SourceCommit  string
	RosterSHA256  string
	RosterGitBlob string
	Slugs         []string
	ProofRoot     string
	// Layout is the declaration. Zero keeps HistoricalRoleLayout.
	Layout RoleLayout
	// Files is keyed by slug, then by file name.
	Files map[string]map[string]PinnedFile
}

type rosterAgent struct {
	Slug      string `yaml:"slug"`
	Role      string `yaml:"role"`
	ReportsTo string `yaml:"reports_to"`
	DottedTo  string `yaml:"dotted_to"`
}

// LoadPinnedRoles reads one explicit selection and returns the native tree
// that OpenNativeTree freezes. It does not open a policy or a queue row.
// File text is not a grant, a schedule, or a model. The declared root is
// the one seat whose frozen reportsTo is empty. Depth does not classify it.
func LoadPinnedRoles(in PinnedRoles) (NativeTree, error) {
	if err := checkPinnedRoot(in.Root); err != nil {
		return NativeTree{}, err
	}
	NoteRoleCheckout(in.Root)
	if in.SourceCommit == "" || in.RosterSHA256 == "" || in.RosterGitBlob == "" {
		return NativeTree{}, fmt.Errorf("%w: source pin", ErrPinnedRoles)
	}
	explicit := in.Layout.Declared()
	layout := in.Layout
	if !explicit {
		layout = HistoricalRoleLayout()
	} else {
		if layout.MaxDepth < 1 {
			layout.MaxDepth = DefaultNativeMaxDepth
		}
		if layout.AbsoluteMaxDepth < 1 {
			layout.AbsoluteMaxDepth = DefaultNativeAbsoluteMaxDepth
		}
	}
	if msg := seatFilesOK(layout.Files); msg != "" || !pathTemplateOK(layout.PathTemplate) || !stableRelPath(layout.RosterPath) {
		return NativeTree{}, fmt.Errorf("%w: layout", ErrPinnedRoles)
	}
	rootSlug := layout.Root
	if rootSlug == "" {
		rootSlug = in.ProofRoot
	}
	selected, err := selectedSlugs(in.Slugs)
	if err != nil {
		return NativeTree{}, err
	}
	if _, ok := selected[rootSlug]; !ok {
		return NativeTree{}, fmt.Errorf("%w: root", ErrPinnedRoles)
	}
	rosterBody, err := readPinned(in.Root, layout.RosterPath, pinnedRosterCap)
	if err != nil {
		return NativeTree{}, err
	}
	// The git blob header includes the roster length, so a short read cannot match.
	if err := matchPin("roster", rosterBody, PinnedFile{SHA256: in.RosterSHA256, GitBlob: in.RosterGitBlob, Size: len(rosterBody)}); err != nil {
		return NativeTree{}, err
	}
	agents, err := parseRoster(rosterBody)
	if err != nil {
		return NativeTree{}, err
	}
	for slug := range selected {
		if _, ok := agents[slug]; !ok {
			return NativeTree{}, fmt.Errorf("%w: unknown slug %s", ErrPinnedRoles, slug)
		}
	}
	if err := projectSelection(rootSlug, in.Slugs, selected, agents); err != nil {
		return NativeTree{}, err
	}
	personas := make([]NativePersona, 0, len(in.Slugs))
	for _, slug := range in.Slugs {
		persona, err := loadPersona(in, layout, rootSlug, agents[slug])
		if err != nil {
			return NativeTree{}, err
		}
		personas = append(personas, persona)
	}
	impl, rev := layout.Implementer, layout.Reviewer
	if !explicit {
		impl, rev = "", ""
	}
	tree := NativeTree{
		NativeHierarchy:       true,
		Synthetic:             true,
		AtumRoster:            false,
		AllowAncestorShortcut: false,
		Delegation:            "direct_manager",
		MaxDepth:              layout.MaxDepth,
		AbsoluteMaxDepth:      layout.AbsoluteMaxDepth,
		Personas:              personas,
		SourceCommit:          in.SourceCommit,
		RosterSHA256:          in.RosterSHA256,
		RosterGitBlob:         in.RosterGitBlob,
		BundleMode:            BundleModeRoleFiles,
		Implementer:           impl,
		Reviewer:              rev,
		RosterPath:            layout.RosterPath,
		PathTemplate:          layout.PathTemplate,
		SeatFiles:             append([]string{}, layout.Files...),
	}
	if _, err := ValidateNativeTree(tree); err != nil {
		return NativeTree{}, err
	}
	return tree, nil
}

// NativeSeatPrompt is the activation prompt for one assignee.
// A policy with no role files keeps the literal native activation prompt.
// A policy with role files returns that assignee's four files, or an error
// before a run is queued.
func NativeSeatPrompt(personas []NativePersona, assignee string) (string, error) {
	bundled := false
	for _, persona := range personas {
		if len(persona.Files) > 0 {
			bundled = true
			break
		}
	}
	if !bundled {
		return "native activation", nil
	}
	var seat *NativePersona
	for i := range personas {
		if personas[i].Name == assignee {
			seat = &personas[i]
			break
		}
	}
	names, ok := seatFileNames(personas)
	if seat == nil || !ok || len(seat.Files) != len(names) {
		return "", fmt.Errorf("%w: seat files", ErrPinnedRoles)
	}
	var b strings.Builder
	b.WriteString("# Seat\n")
	b.WriteString(assignee)
	b.WriteByte('\n')
	for i, name := range names {
		file := seat.Files[i]
		if file.Name != name {
			return "", fmt.Errorf("%w: seat file order", ErrPinnedRoles)
		}
		fmt.Fprintf(&b, "# File %s\nbytes: %d\n", name, len(file.Bytes))
		b.Write(file.Bytes)
	}
	return b.String(), nil
}

// NativeGoal is the invocation objective placed beside one seat's files.
// The bytes are task text. They do not grant secrets, repositories, or tokens.
type NativeGoal struct {
	WorkItemID    string
	InvocationID  string
	TaskID        string
	Repository    string
	PinnedHeadSHA string
	Title         string
	Body          string
}

// CheckBundle accepts a legacy tree or one complete role_files bundle.
// A complete pinned tree with no mode is inferred as role_files. Pins with
// no seat files, an unknown mode, or a short seat are errors. The returned
// tree carries the effective mode and does not trim file bytes.
func CheckBundle(tree NativeTree) (NativeTree, error) {
	mode, err := effectiveBundleMode(tree)
	if err != nil {
		return NativeTree{}, err
	}
	tree.BundleMode = mode
	return tree, nil
}

// AppendNativeGoal appends a length-prefixed goal after the exact seat bytes.
// Title and body are copied raw. A bundled claim with an empty field, a pin
// that is not 40 hex, or a title or body over NativeGoalBodyCap fails.
func AppendNativeGoal(seat string, goal NativeGoal) (string, error) {
	if err := validateNativeGoal(goal); err != nil {
		return "", err
	}
	var b strings.Builder
	b.Grow(len(seat) + len(goal.Title) + len(goal.Body) + 256)
	b.WriteString(seat)
	b.WriteString("# Goal\n")
	writeGoalID(&b, "workItem", goal.WorkItemID)
	writeGoalID(&b, "invocation", goal.InvocationID)
	writeGoalID(&b, "task", goal.TaskID)
	writeGoalID(&b, "repository", goal.Repository)
	writeGoalID(&b, "pin", goal.PinnedHeadSHA)
	writeGoalBytes(&b, "Title", goal.Title)
	writeGoalBytes(&b, "Body", goal.Body)
	return b.String(), nil
}

func effectiveBundleMode(tree NativeTree) (string, error) {
	complete, anyFiles := seatFilesComplete(tree.Personas)
	pins := tree.SourceCommit != "" || tree.RosterSHA256 != "" || tree.RosterGitBlob != ""
	mode := tree.BundleMode
	if mode == "" && !pins && !anyFiles {
		return "", nil
	}
	if mode == "" {
		mode = BundleModeRoleFiles
	}
	if mode != BundleModeRoleFiles {
		return "", fmt.Errorf("%w: unknown bundle mode", ErrPinnedRoles)
	}
	if tree.SourceCommit == "" || tree.RosterSHA256 == "" || tree.RosterGitBlob == "" || !complete {
		return "", fmt.Errorf("%w: bundle metadata", ErrPinnedRoles)
	}
	return mode, nil
}

func seatFilesComplete(personas []NativePersona) (complete, anyFiles bool) {
	names, ok := seatFileNames(personas)
	if !ok {
		return false, true
	}
	if names == nil {
		return false, false
	}
	if len(names) < 1 || len(names) > maxSeatFiles {
		return false, true
	}
	for _, persona := range personas {
		if len(persona.Files) == 0 {
			return false, true
		}
	}
	return true, true
}

func seatFileNames(personas []NativePersona) ([]string, bool) {
	var names []string
	for _, persona := range personas {
		if len(persona.Files) == 0 {
			continue
		}
		got := make([]string, len(persona.Files))
		for i, file := range persona.Files {
			if !singleSegment(file.Name) {
				return nil, false
			}
			got[i] = file.Name
		}
		if names == nil {
			names = got
			continue
		}
		if len(names) != len(got) {
			return nil, false
		}
		for i := range names {
			if names[i] != got[i] {
				return nil, false
			}
		}
	}
	return names, true
}

func validateNativeGoal(goal NativeGoal) error {
	if goal.WorkItemID == "" || goal.InvocationID == "" || goal.TaskID == "" || goal.Repository == "" || goal.Title == "" || goal.Body == "" {
		return fmt.Errorf("%w: goal", ErrPinnedRoles)
	}
	if !commitHex(goal.PinnedHeadSHA) {
		return fmt.Errorf("%w: pin", ErrPinnedRoles)
	}
	if len(goal.Body) > NativeGoalBodyCap || len(goal.Title) > NativeGoalBodyCap {
		return fmt.Errorf("%w: goal cap %d", ErrPinnedRoles, NativeGoalBodyCap)
	}
	for _, id := range []string{goal.WorkItemID, goal.InvocationID, goal.TaskID, goal.Repository, goal.PinnedHeadSHA} {
		if strings.ContainsAny(id, "\r\n") {
			return fmt.Errorf("%w: goal identity", ErrPinnedRoles)
		}
	}
	return nil
}

func commitHex(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func writeGoalID(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteByte('\n')
}

func writeGoalBytes(b *strings.Builder, name, value string) {
	fmt.Fprintf(b, "# %s\nbytes: %d\n", name, len(value))
	b.WriteString(value)
}

func checkPinnedRoot(root string) error {
	if root == "" {
		return fmt.Errorf("%w: root", ErrPinnedRoles)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("%w: root: %w", ErrPinnedRoles, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symlink root", ErrPinnedRoles)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: root is not a directory", ErrPinnedRoles)
	}
	return nil
}

func selectedSlugs(slugs []string) (map[string]struct{}, error) {
	if len(slugs) == 0 {
		return nil, fmt.Errorf("%w: empty selection", ErrPinnedRoles)
	}
	selected := make(map[string]struct{}, len(slugs))
	for _, slug := range slugs {
		if !validSlug(slug) {
			return nil, fmt.Errorf("%w: slug %q", ErrPinnedRoles, slug)
		}
		if _, ok := selected[slug]; ok {
			return nil, fmt.Errorf("%w: duplicate slug %s", ErrPinnedRoles, slug)
		}
		selected[slug] = struct{}{}
	}
	return selected, nil
}

func validSlug(slug string) bool {
	if slug == "" || slug == "." || slug == ".." {
		return false
	}
	if filepath.IsAbs(slug) || strings.ContainsAny(slug, `/\`) || strings.Contains(slug, "..") {
		return false
	}
	return filepath.Clean(slug) == slug
}

func projectSelection(root string, slugs []string, selected map[string]struct{}, agents map[string]rosterAgent) error {
	for _, slug := range slugs {
		agent := agents[slug]
		if agent.DottedTo != "" {
			return fmt.Errorf("%w: dotted edge %s", ErrPinnedRoles, slug)
		}
		if slug == root {
			continue
		}
		if _, inSet := selected[agent.ReportsTo]; !inSet {
			return fmt.Errorf("%w: manager outside selection %s", ErrPinnedRoles, slug)
		}
	}
	return nil
}

func loadPersona(in PinnedRoles, layout RoleLayout, root string, agent rosterAgent) (NativePersona, error) {
	pins := in.Files[agent.Slug]
	if len(pins) != len(layout.Files) {
		return NativePersona{}, fmt.Errorf("%w: file pins %s", ErrPinnedRoles, agent.Slug)
	}
	files := make([]RoleFile, 0, len(layout.Files))
	for _, name := range layout.Files {
		pin, ok := pins[name]
		if !ok {
			return NativePersona{}, fmt.Errorf("%w: missing %s/%s", ErrPinnedRoles, agent.Slug, name)
		}
		rel, ok := ExpandRolePath(layout, agent.Slug, name)
		if !ok {
			return NativePersona{}, fmt.Errorf("%w: path %s/%s", ErrPinnedRoles, agent.Slug, name)
		}
		body, err := readPinned(in.Root, rel, pinnedRoleFileCap)
		if err != nil {
			return NativePersona{}, err
		}
		if err := matchPin(rel, body, pin); err != nil {
			return NativePersona{}, err
		}
		files = append(files, RoleFile{Name: name, SHA256: pin.SHA256, GitBlob: pin.GitBlob, Bytes: body})
	}
	persona := NativePersona{
		Name:            agent.Slug,
		Role:            agent.Role,
		SourceReportsTo: agent.ReportsTo,
		Files:           files,
	}
	if agent.Slug == root {
		return persona, nil
	}
	persona.ReportsTo = agent.ReportsTo
	persona.SpawnableBy = []string{agent.ReportsTo}
	return persona, nil
}

func parseRoster(body []byte) (map[string]rosterAgent, error) {
	agents, err := decodeRoster(body)
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return nil, fmt.Errorf("%w: roster agents", ErrPinnedRoles)
	}
	bySlug := make(map[string]rosterAgent, len(agents))
	for _, agent := range agents {
		if !validSlug(agent.Slug) {
			return nil, fmt.Errorf("%w: roster slug", ErrPinnedRoles)
		}
		if _, ok := bySlug[agent.Slug]; ok {
			return nil, fmt.Errorf("%w: duplicate roster slug %s", ErrPinnedRoles, agent.Slug)
		}
		if !roleNameOK(agent.Role) {
			return nil, fmt.Errorf("%w: role %s", ErrPinnedRoles, agent.Slug)
		}
		bySlug[agent.Slug] = agent
	}
	for _, agent := range bySlug {
		if agent.ReportsTo != "" {
			if _, ok := bySlug[agent.ReportsTo]; !ok {
				return nil, fmt.Errorf("%w: unknown manager %s", ErrPinnedRoles, agent.Slug)
			}
		}
		if agent.DottedTo != "" {
			if _, ok := bySlug[agent.DottedTo]; !ok {
				return nil, fmt.Errorf("%w: unknown dotted %s", ErrPinnedRoles, agent.Slug)
			}
		}
	}
	if err := rosterCycles(bySlug); err != nil {
		return nil, err
	}
	return bySlug, nil
}

func decodeRoster(body []byte) ([]rosterAgent, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: roster: %w", ErrPinnedRoles, err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: roster shape", ErrPinnedRoles)
	}
	var agents *yaml.Node
	mapping := doc.Content[0]
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		val := mapping.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Value != "agents" {
			return nil, fmt.Errorf("%w: roster shape", ErrPinnedRoles)
		}
		if agents != nil || val.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("%w: roster shape", ErrPinnedRoles)
		}
		agents = val
	}
	if agents == nil {
		return nil, fmt.Errorf("%w: roster shape", ErrPinnedRoles)
	}
	out := make([]rosterAgent, 0, len(agents.Content))
	for _, item := range agents.Content {
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%w: roster shape", ErrPinnedRoles)
		}
		var agent rosterAgent
		for i := 0; i+1 < len(item.Content); i += 2 {
			key := item.Content[i]
			val := item.Content[i+1]
			if key.Kind != yaml.ScalarNode || val.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("%w: roster shape", ErrPinnedRoles)
			}
			switch key.Value {
			case "slug":
				agent.Slug = val.Value
			case "role":
				agent.Role = val.Value
			case "reports_to":
				agent.ReportsTo = val.Value
			case "dotted_to":
				agent.DottedTo = val.Value
			default:
				return nil, fmt.Errorf("%w: roster shape", ErrPinnedRoles)
			}
		}
		out = append(out, agent)
	}
	return out, nil
}

func roleNameOK(role string) bool {
	if role == "" || len(role) > 64 {
		return false
	}
	for i, c := range role {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

func singleSegment(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && !strings.Contains(name, "\x00")
}

// ExpandRolePath substitutes the declaration. An empty file selects the roster path.
func ExpandRolePath(layout RoleLayout, slug, file string) (string, bool) {
	if !layout.Declared() {
		return "", false
	}
	if file == "" {
		if !stableRelPath(layout.RosterPath) {
			return "", false
		}
		return layout.RosterPath, true
	}
	if !singleSegment(slug) || !singleSegment(file) || strings.Count(layout.PathTemplate, "{slug}") != 1 || strings.Count(layout.PathTemplate, "{file}") != 1 {
		return "", false
	}
	rel := strings.ReplaceAll(strings.ReplaceAll(layout.PathTemplate, "{slug}", slug), "{file}", file)
	if !stableRelPath(rel) {
		return "", false
	}
	for _, name := range layout.Files {
		if name == file {
			return rel, true
		}
	}
	return rel, false
}

var (
	notedMu   sync.Mutex
	notedDirs []string
)

// NoteRoleCheckout remembers a local checkout. Attest can find a commit there
// when the per-factory cache has no entry. The compare still does not fetch.
func NoteRoleCheckout(dir string) {
	if dir == "" {
		return
	}
	notedMu.Lock()
	defer notedMu.Unlock()
	for _, have := range notedDirs {
		if have == dir {
			return
		}
	}
	notedDirs = append(notedDirs, dir)
}

// RoleCheckoutFor returns a noted checkout that contains the object.
func RoleCheckoutFor(ctx context.Context, commit string) (string, bool) {
	notedMu.Lock()
	dirs := append([]string{}, notedDirs...)
	notedMu.Unlock()
	for _, dir := range dirs {
		if _, ok := GitObjectKind(ctx, dir, commit); ok {
			return dir, true
		}
	}
	return "", false
}

func rosterCycles(agents map[string]rosterAgent) error {
	for start := range agents {
		seen := map[string]struct{}{}
		cursor := start
		for {
			if _, ok := seen[cursor]; ok {
				return fmt.Errorf("%w: cycle", ErrPinnedRoles)
			}
			seen[cursor] = struct{}{}
			parent := agents[cursor].ReportsTo
			if parent == "" {
				break
			}
			cursor = parent
			if len(seen) > len(agents) {
				return fmt.Errorf("%w: cycle", ErrPinnedRoles)
			}
		}
	}
	return nil
}

func readPinned(root, rel string, limit int) ([]byte, error) {
	if filepath.IsAbs(rel) || rel == "" || filepath.Clean(rel) != rel || strings.Contains(rel, "..") {
		return nil, fmt.Errorf("%w: path %s", ErrPinnedRoles, rel)
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	relBack, err := filepath.Rel(root, full)
	if err != nil || relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: path %s", ErrPinnedRoles, rel)
	}
	info, err := os.Lstat(full)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrPinnedRoles, rel, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: symlink %s", ErrPinnedRoles, rel)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a file", ErrPinnedRoles, rel)
	}
	if info.Size() > int64(limit) {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrPinnedRoles, rel, limit)
	}
	body, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrPinnedRoles, rel, err)
	}
	if len(body) > limit {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrPinnedRoles, rel, limit)
	}
	if err := resolvedInside(root, full, rel); err != nil {
		return nil, err
	}
	return body, nil
}

func resolvedInside(root, full, rel string) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("%w: root: %w", ErrPinnedRoles, err)
	}
	fullReal, err := filepath.EvalSymlinks(full)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrPinnedRoles, rel, err)
	}
	relReal, err := filepath.Rel(rootReal, fullReal)
	if err != nil || relReal == ".." || strings.HasPrefix(relReal, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: path %s", ErrPinnedRoles, rel)
	}
	return nil
}

func matchPin(label string, body []byte, pin PinnedFile) error {
	if pin.Size != len(body) {
		return fmt.Errorf("%w: size %s", ErrPinnedRoles, label)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != pin.SHA256 {
		return fmt.Errorf("%w: sha256 %s", ErrPinnedRoles, label)
	}
	if gitBlobID(body) != pin.GitBlob {
		return fmt.Errorf("%w: git blob %s", ErrPinnedRoles, label)
	}
	return nil
}

func gitBlobID(body []byte) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(body))
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
