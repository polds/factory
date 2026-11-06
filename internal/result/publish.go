package result

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxReportBytes   = 1 << 20
	maxArtifactBytes = 16 << 20

	CodePathRejected    = "result_path_rejected"
	CodeSymlinkRejected = "result_symlink_rejected"
	CodeOversize        = "result_oversize"
	CodeSecretRejected  = "secret_publication_rejected"
	CodeUnauthorized    = "result_unauthorized"
	CodeReportMissing   = "report_missing"
)

// PublishError is a publication guard failure; nothing is stored.
type PublishError struct {
	Code string
}

func (e PublishError) Error() string { return e.Code }

// Report is the durable product validated at terminal time.
type Report struct {
	Summary        string
	CoverageStatus string
	FindingsCount  int
	Body           []byte
	ArtifactPaths  []string
}

// ValidatePath rejects absolute paths, backslashes, and .. segments.
func ValidatePath(rel string) error {
	if rel == "" {
		return PublishError{Code: CodePathRejected}
	}
	if filepath.IsAbs(rel) || strings.Contains(rel, `\`) {
		return PublishError{Code: CodePathRejected}
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return PublishError{Code: CodePathRejected}
	}
	return nil
}

// ValidateSymlink rejects symlink paths under root.
func ValidateSymlink(root, rel string) error {
	if err := ValidatePath(rel); err != nil {
		return err
	}
	full := filepath.Join(root, rel)
	fi, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return PublishError{Code: CodeSymlinkRejected}
	}
	return nil
}

// ValidateSize enforces report and artifact caps.
func ValidateSize(report []byte, artifacts []string, root string) error {
	if len(report) > maxReportBytes {
		return PublishError{Code: CodeOversize}
	}
	for _, rel := range artifacts {
		if err := ValidatePath(rel); err != nil {
			return err
		}
		fi, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if fi.Size() > maxArtifactBytes {
			return PublishError{Code: CodeOversize}
		}
	}
	return nil
}

// ScanSecrets rejects bodies containing synthetic canary labels used in witnesses.
func ScanSecrets(body []byte) error {
	text := string(body)
	for _, label := range []string{"CANARY-OLD", "CANARY-NEW", "CANARY-SYNTHETIC"} {
		if strings.Contains(text, label) {
			return PublishError{Code: CodeSecretRejected}
		}
	}
	return nil
}

// Publish validates authorization, path, symlink, size, and secret scan.
// Artifact paths require a workspace root; an empty root fails closed.
func Publish(authorized bool, root string, rep Report) error {
	if !authorized {
		return PublishError{Code: CodeUnauthorized}
	}
	if len(rep.ArtifactPaths) > 0 && strings.TrimSpace(root) == "" {
		return PublishError{Code: CodePathRejected}
	}
	for _, rel := range rep.ArtifactPaths {
		if err := ValidatePath(rel); err != nil {
			return err
		}
		if err := ValidateSymlink(root, rel); err != nil {
			return err
		}
		full := filepath.Join(root, rel)
		body, err := os.ReadFile(full)
		if err != nil {
			return PublishError{Code: CodePathRejected}
		}
		if len(body) > maxArtifactBytes {
			return PublishError{Code: CodeOversize}
		}
		if err := ScanSecrets(body); err != nil {
			return err
		}
	}
	if err := ValidateSize(rep.Body, nil, root); err != nil {
		return err
	}
	if err := ScanSecrets(rep.Body); err != nil {
		return err
	}
	return nil
}

// ValidateTerminal reports whether a report requirement is satisfied.
func ValidateTerminal(requiresReport bool, present bool, findings int, coverage string) error {
	if !requiresReport {
		return nil
	}
	if !present {
		return errors.New(CodeReportMissing)
	}
	if findings == 0 && coverage == "" {
		return errors.New(CodeReportMissing)
	}
	return nil
}
