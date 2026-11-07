package definition

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// Pinned role files, in prompt and digest order.
var pinnedRoleFiles = []string{"AGENTS.md", "HEARTBEAT.md", "SOUL.md", "TOOLS.md"}

var pinnedSourceRoles = map[string]struct{}{
	"ceo": {}, "cto": {}, "manager": {}, "general": {}, "worker": {},
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
// Synthetic is the existing roster gate. It does not mean the source
// organization executed, and file text is not a grant, a schedule, or a model.
func LoadPinnedRoles(in PinnedRoles) (NativeTree, error) {
	if err := checkPinnedRoot(in.Root); err != nil {
		return NativeTree{}, err
	}
	if in.SourceCommit == "" || in.RosterSHA256 == "" || in.RosterGitBlob == "" {
		return NativeTree{}, fmt.Errorf("%w: source pin", ErrPinnedRoles)
	}
	selected, err := selectedSlugs(in.Slugs)
	if err != nil {
		return NativeTree{}, err
	}
	if _, ok := selected[in.ProofRoot]; !ok {
		return NativeTree{}, fmt.Errorf("%w: proof root", ErrPinnedRoles)
	}
	rosterBody, err := readPinned(in.Root, rosterRel, pinnedRosterCap)
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
	if err := projectSelection(in, selected, agents); err != nil {
		return NativeTree{}, err
	}
	personas := make([]NativePersona, 0, len(in.Slugs))
	for _, slug := range in.Slugs {
		persona, err := loadPersona(in, agents[slug])
		if err != nil {
			return NativeTree{}, err
		}
		personas = append(personas, persona)
	}
	tree := NativeTree{
		NativeHierarchy:       true,
		Synthetic:             true,
		AtumRoster:            false,
		AllowAncestorShortcut: false,
		Delegation:            "direct_manager",
		MaxDepth:              NativeProofMaxDepth,
		AbsoluteMaxDepth:      NativeAbsoluteMaxDepth,
		Personas:              personas,
		SourceCommit:          in.SourceCommit,
		RosterSHA256:          in.RosterSHA256,
		RosterGitBlob:         in.RosterGitBlob,
		BundleMode:            BundleModeRoleFiles,
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
	if seat == nil || len(seat.Files) != len(pinnedRoleFiles) {
		return "", fmt.Errorf("%w: seat files", ErrPinnedRoles)
	}
	var b strings.Builder
	b.WriteString("# Seat\n")
	b.WriteString(assignee)
	b.WriteByte('\n')
	for i, name := range pinnedRoleFiles {
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
	complete = true
	for _, persona := range personas {
		if len(persona.Files) == 0 {
			complete = false
			continue
		}
		anyFiles = true
		if len(persona.Files) != len(pinnedRoleFiles) {
			return false, true
		}
		for i, name := range pinnedRoleFiles {
			if persona.Files[i].Name != name {
				return false, true
			}
		}
	}
	if !anyFiles {
		complete = false
	}
	return complete, anyFiles
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

func projectSelection(in PinnedRoles, selected map[string]struct{}, agents map[string]rosterAgent) error {
	var outside string
	outsideCount := 0
	for _, slug := range in.Slugs {
		agent := agents[slug]
		if agent.DottedTo != "" {
			return fmt.Errorf("%w: dotted edge %s", ErrPinnedRoles, slug)
		}
		if _, inSet := selected[agent.ReportsTo]; !inSet {
			outside = slug
			outsideCount++
		}
	}
	if outsideCount != 1 || outside != in.ProofRoot {
		return fmt.Errorf("%w: proof root is not the outside manager", ErrPinnedRoles)
	}
	for _, slug := range in.Slugs {
		if slug == in.ProofRoot {
			continue
		}
		parent := agents[agents[slug].ReportsTo]
		if parent.Role == "general" || parent.Role == "worker" {
			return fmt.Errorf("%w: projected parent %s", ErrPinnedRoles, agents[slug].ReportsTo)
		}
	}
	return nil
}

func loadPersona(in PinnedRoles, agent rosterAgent) (NativePersona, error) {
	pins := in.Files[agent.Slug]
	if len(pins) != len(pinnedRoleFiles) {
		return NativePersona{}, fmt.Errorf("%w: file pins %s", ErrPinnedRoles, agent.Slug)
	}
	files := make([]RoleFile, 0, len(pinnedRoleFiles))
	for _, name := range pinnedRoleFiles {
		pin, ok := pins[name]
		if !ok {
			return NativePersona{}, fmt.Errorf("%w: missing %s/%s", ErrPinnedRoles, agent.Slug, name)
		}
		rel := "org/agents/" + agent.Slug + "/" + name
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
	if agent.Slug == in.ProofRoot {
		return persona, nil
	}
	persona.ReportsTo = agent.ReportsTo
	persona.SpawnableBy = []string{agent.ReportsTo}
	return persona, nil
}

func parseRoster(body []byte) (map[string]rosterAgent, error) {
	if err := requireRosterShape(body); err != nil {
		return nil, err
	}
	var doc struct {
		Agents []rosterAgent `yaml:"agents"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: roster: %w", ErrPinnedRoles, err)
	}
	if len(doc.Agents) == 0 {
		return nil, fmt.Errorf("%w: roster agents", ErrPinnedRoles)
	}
	agents := make(map[string]rosterAgent, len(doc.Agents))
	for _, agent := range doc.Agents {
		if !validSlug(agent.Slug) {
			return nil, fmt.Errorf("%w: roster slug", ErrPinnedRoles)
		}
		if _, ok := agents[agent.Slug]; ok {
			return nil, fmt.Errorf("%w: duplicate roster slug %s", ErrPinnedRoles, agent.Slug)
		}
		if _, ok := pinnedSourceRoles[agent.Role]; !ok {
			return nil, fmt.Errorf("%w: role %s", ErrPinnedRoles, agent.Slug)
		}
		agents[agent.Slug] = agent
	}
	for _, agent := range agents {
		if agent.ReportsTo != "" {
			if _, ok := agents[agent.ReportsTo]; !ok {
				return nil, fmt.Errorf("%w: unknown manager %s", ErrPinnedRoles, agent.Slug)
			}
		}
		if agent.DottedTo != "" {
			if _, ok := agents[agent.DottedTo]; !ok {
				return nil, fmt.Errorf("%w: unknown dotted %s", ErrPinnedRoles, agent.Slug)
			}
		}
	}
	if err := rosterCycles(agents); err != nil {
		return nil, err
	}
	return agents, nil
}

func requireRosterShape(body []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("%w: roster: %w", ErrPinnedRoles, err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%w: roster shape", ErrPinnedRoles)
	}
	var hasMeta, hasAgents bool
	mapping := doc.Content[0]
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		val := mapping.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			continue
		}
		switch key.Value {
		case "meta":
			hasMeta = val.Kind == yaml.MappingNode && val.Tag != "!!null"
		case "agents":
			hasAgents = val.Kind == yaml.SequenceNode
		}
	}
	if !hasMeta || !hasAgents {
		return fmt.Errorf("%w: roster shape", ErrPinnedRoles)
	}
	return nil
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
