package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// envFlag collects repeatable -env KEY=VALUE flags. String reports keys only
// so a usage line cannot write secret values.
type envFlag struct {
	m map[string]string
}

func (e *envFlag) String() string {
	if e == nil || len(e.m) == 0 {
		return ""
	}
	return strings.Join(sortedKeys(e.m), ",")
}

func (e *envFlag) Set(s string) error {
	key, val, ok := strings.Cut(s, "=")
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return fmt.Errorf("env must be KEY=VALUE")
	}
	if e.m == nil {
		e.m = make(map[string]string)
	}
	e.m[key] = val
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// loadEnvFile reads dotenv lines (KEY=VALUE). Blank lines and full-line
// # comments are ignored. A later line for the same key wins. Error text
// names the file and line number and never includes the line itself.
func loadEnvFile(path string) (m map[string]string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()

	m = make(map[string]string)
	sc := bufio.NewScanner(f)
	// FACTORY_TASK_PROMPT can be longer than the default 64KiB token limit.
	sc.Buffer(make([]byte, 0, 64*1024), 10<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("%s:%d: invalid env line (want KEY=VALUE)", path, lineNo)
		}
		m[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// mergeEnv loads path (when set) and then applies overrides. Keys in
// overrides win, which is how -env beats -env-file.
func mergeEnv(path string, overrides map[string]string) (map[string]string, error) {
	merged := make(map[string]string)
	if path != "" {
		loaded, err := loadEnvFile(path)
		if err != nil {
			return nil, err
		}
		merged = loaded
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return merged, nil
}
