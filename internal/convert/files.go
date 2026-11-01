package convert

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SourceFile is one definition-tree file. Path is slash-separated and
// relative to the tree root.
type SourceFile struct {
	Path    string
	Content string
}

// ReadFiles returns the definition files LoadDir reads, in path order.
// README and other non-definition files are not included. Content is the
// raw file bytes; callers that emit manifests must redact credential values.
func ReadFiles(root string) ([]SourceFile, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return nil, fmt.Errorf("read definition: %w", err)
	}
	var out []SourceFile
	add := func(rel string) error {
		rel = filepath.ToSlash(rel)
		if err := safeRel(rel); err != nil {
			return err
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		out = append(out, SourceFile{Path: rel, Content: string(body)})
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, "factory.yaml")); err == nil {
		if err := add("factory.yaml"); err != nil {
			return nil, err
		}
	}
	if err := addFlat(root, "runners", ".yaml", add); err != nil {
		return nil, err
	}
	if err := addFlat(root, "routers", ".yaml", add); err != nil {
		return nil, err
	}
	if err := addFlat(root, "webhooks", ".yaml", add); err != nil {
		return nil, err
	}
	if err := addNested(root, "agents", "agent.md", add); err != nil {
		return nil, err
	}
	if err := addAgentSkills(root, add); err != nil {
		return nil, err
	}
	if err := addNested(root, "automations", "automation.md", add); err != nil {
		return nil, err
	}
	if err := addNested(root, "scorers", "scorer.md", add); err != nil {
		return nil, err
	}
	if err := addNested(root, "skills", "SKILL.md", add); err != nil {
		return nil, err
	}
	if err := addBenchmarks(root, add); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func addFlat(root, dir, suffix string, add func(string) error) error {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		if err := add(dir + "/" + e.Name()); err != nil {
			return err
		}
	}
	return nil
}

func addNested(root, dir, file string, add func(string) error) error {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := dir + "/" + e.Name() + "/" + file
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := add(rel); err != nil {
			return err
		}
	}
	return nil
}

func addAgentSkills(root string, add func(string) error) error {
	agents := filepath.Join(root, "agents")
	entries, err := os.ReadDir(agents)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, agent := range entries {
		if !agent.IsDir() {
			continue
		}
		skillRoot := filepath.Join(agents, agent.Name(), "skills")
		skills, err := os.ReadDir(skillRoot)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, skill := range skills {
			if !skill.IsDir() {
				continue
			}
			rel := "agents/" + agent.Name() + "/skills/" + skill.Name() + "/SKILL.md"
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			if err := add(rel); err != nil {
				return err
			}
		}
	}
	return nil
}

func addBenchmarks(root string, add func(string) error) error {
	dir := filepath.Join(root, "benchmarks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		suite := "benchmarks/" + e.Name() + "/suite.yaml"
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(suite))); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		} else if err := add(suite); err != nil {
			return err
		}
		tasks := filepath.Join(dir, e.Name(), "tasks")
		taskEntries, err := os.ReadDir(tasks)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, task := range taskEntries {
			if task.IsDir() || !strings.HasSuffix(task.Name(), ".yaml") {
				continue
			}
			if err := add("benchmarks/" + e.Name() + "/tasks/" + task.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteFiles writes definition files under dest. Paths must stay inside dest.
func WriteFiles(dest string, files []SourceFile) error {
	for _, file := range files {
		if err := safeRel(file.Path); err != nil {
			return err
		}
		abs := filepath.Join(dest, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, []byte(file.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func safeRel(rel string) error {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\\") {
		return fmt.Errorf("definition path %q is not relative", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("definition path %q escapes the tree", rel)
	}
	return nil
}
