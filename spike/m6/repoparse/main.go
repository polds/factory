// Command repoparse prints owner/name for definition git URLs.
// The function matches internal/server/m6_tokens.go definitionRepo.
package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
)

func definitionRepo(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("server: definition git url is empty")
	}
	raw = strings.TrimSuffix(raw, ".git")
	path := raw
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		path = strings.Trim(parsed.Path, "/")
	} else if i := strings.LastIndex(raw, ":"); i >= 0 {
		path = strings.Trim(raw[i+1:], "/")
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[len(parts)-1] == "" || parts[len(parts)-2] == "" {
		return "", errors.New("server: definition git url has no repository")
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
}

type result struct {
	In   string `json:"in"`
	Repo string `json:"repo,omitempty"`
	Err  string `json:"err,omitempty"`
}

func main() {
	var inputs []string
	if err := json.NewDecoder(os.Stdin).Decode(&inputs); err != nil {
		os.Exit(2)
	}
	out := make([]result, 0, len(inputs))
	for _, raw := range inputs {
		repo, err := definitionRepo(raw)
		row := result{In: raw, Repo: repo}
		if err != nil {
			row.Err = err.Error()
			row.Repo = ""
		}
		out = append(out, row)
	}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(out); err != nil {
		os.Exit(1)
	}
}
