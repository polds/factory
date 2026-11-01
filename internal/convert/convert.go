// Package convert turns a factory definition tree into factory.dev manifests
// and back. AgentRun and WorkerPool are never converted.
package convert

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	yaml "go.yaml.in/yaml/v3"
	syaml "sigs.k8s.io/yaml"

	"github.com/polds/factory/internal/definition"
)

// Options controls convert DIR.
type Options struct {
	// Namespace is set on each manifest when non-empty.
	Namespace string
	// InstructionsConfigMap emits instruction bodies as ConfigMaps and
	// points instructionsFrom, promptFrom, or rubricFrom at them.
	// spec.files still carries the original file for a byte-identical round trip.
	InstructionsConfigMap bool
}

// DirToManifests reads a definition directory and returns multi-document YAML.
// Clean trees round-trip byte for byte through ManifestsToDir. Inline
// credential values are removed from the YAML and from errors.
func DirToManifests(dir string, opt Options) ([]byte, error) {
	files, err := ReadFiles(dir)
	if err != nil {
		return nil, redactErr(err)
	}
	if len(files) == 0 {
		return nil, errors.New("no definition files")
	}
	loadDir := dir
	changed := false
	for i := range files {
		next := redactText(files[i].Content)
		if next != files[i].Content {
			files[i].Content = next
			changed = true
		}
	}
	if changed {
		tmp, err := os.MkdirTemp("", "factory-convert-")
		if err != nil {
			return nil, redactErr(err)
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		if err := WriteFiles(tmp, files); err != nil {
			return nil, redactErr(err)
		}
		loadDir = tmp
	}
	tree, diags := definition.LoadDir(loadDir)
	if err := errorDiagnostics(diags); err != nil {
		return nil, redactErr(err)
	}
	if tree == nil {
		return nil, errors.New("definition did not load")
	}
	objs, err := project(tree, files, opt)
	if err != nil {
		return nil, redactErr(err)
	}
	out, err := marshalManifests(objs)
	if err != nil {
		return nil, redactErr(err)
	}
	return []byte(redactText(string(out))), nil
}

// ManifestsToDir rebuilds a definition tree from multi-document YAML.
// Resources that carry spec.files are written from those bytes. Other
// definition kinds are synthesized. AgentRun and WorkerPool are ignored.
func ManifestsToDir(manifests []byte, dest string) error {
	docs, err := decodeYAMLDocs(manifests)
	if err != nil {
		return redactErr(err)
	}
	configMaps := map[string]map[string]string{}
	for _, obj := range docs {
		if kindOf(obj) != "ConfigMap" {
			continue
		}
		name, data := configMapData(obj)
		if name != "" {
			configMaps[name] = data
		}
	}
	var files []SourceFile
	for _, obj := range docs {
		kind := kindOf(obj)
		switch kind {
		case "", "ConfigMap", "AgentRun", "WorkerPool", "Namespace", "CustomResourceDefinition", "List":
			continue
		}
		got, err := filesFromDoc(kind, obj, configMaps)
		if err != nil {
			return redactErr(err)
		}
		files = append(files, got...)
	}
	seen := map[string]bool{}
	for _, file := range files {
		if seen[file.Path] {
			return fmt.Errorf("duplicate definition path %s", file.Path)
		}
		seen[file.Path] = true
	}
	if len(files) == 0 {
		return errors.New("manifests contain no definition resources")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	return WriteFiles(dest, files)
}

func errorDiagnostics(diags []definition.Diagnostic) error {
	var b strings.Builder
	n := 0
	for _, diag := range diags {
		if diag.Severity != definition.SeverityError {
			continue
		}
		if n > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s:%d: %s", diag.File, diag.Line, diag.Message)
		n++
	}
	if n == 0 {
		return nil
	}
	return errors.New(b.String())
}

func redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := redactText(err.Error())
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}

func marshalManifests(objs []any) ([]byte, error) {
	var buf bytes.Buffer
	for i, obj := range objs {
		raw, err := syaml.Marshal(obj)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(raw)
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}

func decodeYAMLDocs(in []byte) ([]map[string]any, error) {
	if strings.TrimSpace(string(in)) == "" {
		return nil, errors.New("manifests are empty")
	}
	dec := yaml.NewDecoder(bytes.NewReader(in))
	var docs []map[string]any
	for {
		var obj map[string]any
		err := dec.Decode(&obj)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(obj) == 0 {
			continue
		}
		docs = append(docs, obj)
	}
	if len(docs) == 0 {
		return nil, errors.New("manifests are empty")
	}
	return docs, nil
}

func kindOf(obj map[string]any) string {
	kind, _ := obj["kind"].(string)
	return kind
}
