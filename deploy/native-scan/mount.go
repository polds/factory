package nativescan

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// MountWorkspace gives each item its own mount. perItemMount is true.
// A shared writable ART root is rejected.
const sharedArtRoot = "/art"

var (
	errMount = errors.New("nativescan: mount rejected")
)

// Volume is one mount. Source is the volume name, not a host socket path.
type Volume struct {
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"readOnly"`
}

// ItemMount is the workspace and art pair for one item.
type ItemMount struct {
	ID        string `yaml:"id"`
	Workspace Volume `yaml:"workspace"`
	Art       Volume `yaml:"art"`
}

// Sample is the committed backend shape plus the fields the checker refuses.
type Sample struct {
	DockerSocket           bool        `yaml:"dockerSocket"`
	SharedWritableCheckout bool        `yaml:"sharedWritableCheckout"`
	SharedArtRoot          bool        `yaml:"sharedArtRoot"`
	BroadSecrets           []string    `yaml:"broadSecrets"`
	Items                  []ItemMount `yaml:"items"`
}

// LoadSample reads the embedded hardened sample.
func LoadSample() (Sample, error) {
	raw, err := embedded.ReadFile("fixtures/sample-task.yaml")
	if err != nil {
		return Sample{}, err
	}
	return ParseSample(raw)
}

// ParseSample decodes a sample and rejects insecure mounts.
func ParseSample(raw []byte) (Sample, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var sample Sample
	if err := dec.Decode(&sample); err != nil {
		return Sample{}, err
	}
	if err := ValidateSample(sample); err != nil {
		return Sample{}, err
	}
	return sample, nil
}

// ValidateSample refuses host sockets, a shared writable checkout, a shared
// art root, and broad secret mounts.
func ValidateSample(sample Sample) error {
	if sample.DockerSocket || sample.SharedWritableCheckout || sample.SharedArtRoot {
		return errMount
	}
	if len(sample.BroadSecrets) > 0 {
		return errMount
	}
	if len(sample.Items) == 0 {
		return errMount
	}
	sources := map[string]struct{}{}
	for _, item := range sample.Items {
		if !itemIDRe.MatchString(item.ID) {
			return errMount
		}
		if err := validateVolume(item.ID, item.Workspace, true, sources); err != nil {
			return err
		}
		if err := validateVolume(item.ID, item.Art, false, sources); err != nil {
			return err
		}
		wantArt := "/items/" + item.ID + "/art"
		if item.Art.Target != wantArt {
			return fmt.Errorf("%w: art target", errMount)
		}
		if item.Workspace.Target != "/workspace" || !item.Workspace.ReadOnly {
			return fmt.Errorf("%w: workspace", errMount)
		}
	}
	return nil
}

func validateVolume(itemID string, vol Volume, readOnly bool, sources map[string]struct{}) error {
	if vol.Source == "" || vol.Target == "" {
		return errMount
	}
	if strings.Contains(vol.Source, "docker.sock") || strings.Contains(vol.Target, "docker.sock") {
		return errMount
	}
	if vol.Target == sharedArtRoot || vol.Target == sharedArtRoot+"/" {
		return errMount
	}
	if strings.Contains(vol.Source, "all-factory-secrets") {
		return errMount
	}
	if !strings.HasPrefix(vol.Source, itemID+"-") {
		return fmt.Errorf("%w: source %s", errMount, vol.Source)
	}
	if readOnly != vol.ReadOnly {
		return errMount
	}
	if _, ok := sources[vol.Source]; ok {
		return fmt.Errorf("%w: shared source %s", errMount, vol.Source)
	}
	sources[vol.Source] = struct{}{}
	return nil
}

// ComposeText is the embedded compose sample.
func ComposeText() (string, error) {
	raw, err := embedded.ReadFile("compose.yaml")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ComposeRefusesInsecure reports whether the compose sample mounts a host
// socket, a broad secret, or a shared art root target.
func ComposeRefusesInsecure(text string) error {
	for _, bad := range []string{
		"docker.sock",
		"all-factory-secrets",
		"target: /art",
		"target: /var/run/docker.sock",
	} {
		if strings.Contains(text, bad) {
			return fmt.Errorf("%w: %s", errMount, bad)
		}
	}
	if !strings.Contains(text, "target: /items/item-a/art") || !strings.Contains(text, "target: /items/item-b/art") {
		return fmt.Errorf("%w: per-item art", errMount)
	}
	return nil
}
