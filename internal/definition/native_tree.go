package definition

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const (
	// NativeProofMaxDepth is the depth the synthetic tree must reach and must not pass.
	NativeProofMaxDepth = 2
	// NativeAbsoluteMaxDepth is the deepest task chain native mode will run.
	NativeAbsoluteMaxDepth = 8
)

var (
	// ErrNativeTree is an illegal native roster: not one root, not acyclic, not connected, or not the proof shape.
	ErrNativeTree = errors.New("INV-TREE")
	// ErrLegacyHierarchy means reportsTo is being enforced while native hierarchy is off.
	ErrLegacyHierarchy = errors.New("INV-LEGACY-HIERARCHY")
	// ErrDepthExceeded means the task chain is deeper than the frozen max.
	ErrDepthExceeded = errors.New("depth_exceeded")
	// ErrDelegationDenied means the delegator is not the direct manager in spawnableBy.
	ErrDelegationDenied = errors.New("delegation_denied")
)

// RoleFile is one frozen role file. Bytes are the raw file, not a grant.
type RoleFile struct {
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
	GitBlob string `json:"gitBlob"`
	Bytes   []byte `json:"bytes"`
}

// NativePersona is one frozen roster entry. ReportsTo empty is the root.
// Source fields and Files are empty on the legacy witness tree. omitempty
// keeps that tree's digest stable.
type NativePersona struct {
	Name            string     `json:"name"`
	ReportsTo       string     `json:"reportsTo"`
	SpawnableBy     []string   `json:"spawnableBy"`
	Role            string     `json:"role"`
	SourceReportsTo string     `json:"sourceReportsTo,omitempty"`
	SourceDottedTo  string     `json:"sourceDottedTo,omitempty"`
	Files           []RoleFile `json:"files,omitempty"`
}

// NativeTree is the roster and delegation policy frozen for one invocation.
type NativeTree struct {
	NativeHierarchy       bool
	ReportsToOmitted      bool
	Enforced              bool
	Personas              []NativePersona
	MaxDepth              int
	AbsoluteMaxDepth      int
	AllowAncestorShortcut bool
	AtumRoster            bool
	Delegation            string
	Synthetic             bool
	SourceCommit          string
	RosterSHA256          string
	RosterGitBlob         string
	BundleMode            string
	Implementer           string
	Reviewer              string
	RosterPath            string
	PathTemplate          string
	SeatFiles             []string
}

// NativeTreeResult is the frozen roster. Digest does not change when the definition reloads.
type NativeTreeResult struct {
	Root   string
	Depth  map[string]int
	Digest string
}

// ValidateNativeTree checks the native roster. Legacy factories omit reportsTo
// and are valid while native hierarchy is off. reportsTo omitted is not enforced then.
func ValidateNativeTree(in NativeTree) (NativeTreeResult, error) {
	if !in.NativeHierarchy {
		if in.ReportsToOmitted && in.Enforced {
			return NativeTreeResult{}, ErrLegacyHierarchy
		}
		return NativeTreeResult{}, nil
	}
	if in.AllowAncestorShortcut {
		return NativeTreeResult{}, fmt.Errorf("%w: ancestor shortcut", ErrNativeTree)
	}
	if in.Delegation != "direct_manager" {
		return NativeTreeResult{}, fmt.Errorf("%w: delegation", ErrNativeTree)
	}
	if in.AbsoluteMaxDepth < 1 || in.AbsoluteMaxDepth > NativeAbsoluteMaxDepth {
		return NativeTreeResult{}, fmt.Errorf("%w: absoluteMaxDepth", ErrNativeTree)
	}
	if in.MaxDepth < 1 || in.MaxDepth > in.AbsoluteMaxDepth {
		return NativeTreeResult{}, fmt.Errorf("%w: maxDepth", ErrNativeTree)
	}
	if len(in.Personas) > 256 {
		return NativeTreeResult{}, fmt.Errorf("%w: persona cap", ErrNativeTree)
	}
	byName, root, err := indexPersonas(in.Personas)
	if err != nil {
		return NativeTreeResult{}, err
	}
	depth, err := personaDepths(byName, root)
	if err != nil {
		return NativeTreeResult{}, err
	}
	deepest := 0
	for _, d := range depth {
		if d > deepest {
			deepest = d
		}
	}
	if deepest > in.MaxDepth || deepest > in.AbsoluteMaxDepth {
		return NativeTreeResult{}, fmt.Errorf("%w: depth", ErrNativeTree)
	}
	digest, err := treeDigest(in)
	if err != nil {
		return NativeTreeResult{}, err
	}
	return NativeTreeResult{Root: root, Depth: depth, Digest: digest}, nil
}

func AllowDelegate(target NativePersona, delegator string, ancestors []string, allowAncestor bool) error {
	if delegator == "" || target.Name == "" || delegator == target.Name {
		return ErrDelegationDenied
	}
	direct := target.ReportsTo == delegator && personaContains(target.SpawnableBy, delegator)
	if direct {
		return nil
	}
	if allowAncestor && personaContains(ancestors, delegator) && personaContains(target.SpawnableBy, delegator) {
		return nil
	}
	return ErrDelegationDenied
}

func CheckDepth(depth, maxDepth, absoluteMax int) error {
	if absoluteMax <= 0 {
		absoluteMax = NativeAbsoluteMaxDepth
	}
	if depth > absoluteMax || depth > maxDepth {
		return fmt.Errorf("%w: depth %d", ErrDepthExceeded, depth)
	}
	return nil
}

// ReportsDoNotGrantSecrets returns secrets unchanged. reportsTo does not grant
// secret or repository access.
func ReportsDoNotGrantSecrets(reportsTo string, secrets []string) []string {
	_ = reportsTo
	if secrets == nil {
		return []string{}
	}
	return append([]string{}, secrets...)
}

func indexPersonas(personas []NativePersona) (map[string]NativePersona, string, error) {
	if len(personas) == 0 {
		return nil, "", fmt.Errorf("%w: empty roster", ErrNativeTree)
	}
	byName := make(map[string]NativePersona, len(personas))
	root := ""
	roots := 0
	for _, p := range personas {
		if p.Name == "" {
			return nil, "", fmt.Errorf("%w: persona name", ErrNativeTree)
		}
		if _, ok := byName[p.Name]; ok {
			return nil, "", fmt.Errorf("%w: duplicate persona", ErrNativeTree)
		}
		byName[p.Name] = p
		if p.ReportsTo == "" {
			roots++
			root = p.Name
		}
	}
	if roots != 1 {
		return nil, "", fmt.Errorf("%w: root count %d", ErrNativeTree, roots)
	}
	return byName, root, nil
}

func personaDepths(byName map[string]NativePersona, root string) (map[string]int, error) {
	depth := make(map[string]int, len(byName))
	for name := range byName {
		seen := map[string]struct{}{}
		cursor := name
		steps := 0
		for cursor != root {
			if _, ok := seen[cursor]; ok {
				return nil, fmt.Errorf("%w: cycle", ErrNativeTree)
			}
			seen[cursor] = struct{}{}
			parent := byName[cursor].ReportsTo
			if parent == "" || parent == cursor {
				return nil, fmt.Errorf("%w: disconnected", ErrNativeTree)
			}
			if _, ok := byName[parent]; !ok {
				return nil, fmt.Errorf("%w: missing manager", ErrNativeTree)
			}
			cursor = parent
			steps++
			if steps > NativeAbsoluteMaxDepth {
				return nil, fmt.Errorf("%w: cycle", ErrNativeTree)
			}
		}
		depth[name] = steps
	}
	return depth, nil
}

func treeDigest(in NativeTree) (string, error) {
	personas := append([]NativePersona{}, in.Personas...)
	sort.Slice(personas, func(i, j int) bool { return personas[i].Name < personas[j].Name })
	for i := range personas {
		personas[i].SpawnableBy = append([]string{}, personas[i].SpawnableBy...)
		sort.Strings(personas[i].SpawnableBy)
	}
	body, err := json.Marshal(struct {
		Personas              []NativePersona `json:"personas"`
		MaxDepth              int             `json:"maxDepth"`
		AbsoluteMaxDepth      int             `json:"absoluteMaxDepth"`
		AllowAncestorShortcut bool            `json:"allowAncestorShortcut"`
		Delegation            string          `json:"delegation"`
		AtumRoster            bool            `json:"atumRoster"`
		Synthetic             bool            `json:"synthetic"`
		SourceCommit          string          `json:"sourceCommit,omitempty"`
		RosterSHA256          string          `json:"rosterSha256,omitempty"`
		RosterGitBlob         string          `json:"rosterGitBlob,omitempty"`
		BundleMode            string          `json:"bundleMode,omitempty"`
	}{
		Personas:              personas,
		MaxDepth:              in.MaxDepth,
		AbsoluteMaxDepth:      in.AbsoluteMaxDepth,
		AllowAncestorShortcut: in.AllowAncestorShortcut,
		Delegation:            in.Delegation,
		AtumRoster:            in.AtumRoster,
		Synthetic:             in.Synthetic,
		SourceCommit:          in.SourceCommit,
		RosterSHA256:          in.RosterSHA256,
		RosterGitBlob:         in.RosterGitBlob,
		BundleMode:            in.BundleMode,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func personaContains(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}

// Ancestors walks reportsTo from the persona up to but not including the root's parent.
func Ancestors(byName map[string]NativePersona, name string) []string {
	var out []string
	seen := map[string]struct{}{}
	cursor := byName[name].ReportsTo
	for cursor != "" {
		if _, ok := seen[cursor]; ok {
			return out
		}
		seen[cursor] = struct{}{}
		out = append(out, cursor)
		cursor = byName[cursor].ReportsTo
	}
	return out
}
