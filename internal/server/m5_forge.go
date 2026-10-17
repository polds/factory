package server

import (
	"net/url"
	"strings"
)

// forgeTreeURL is the tree link for a git-backed definition.
// It is empty when the URL is not an https, http, or ssh forge, the sha
// is empty, or the subdir is unsafe. Userinfo, query, and fragment are dropped.
func forgeTreeURL(raw, sha, subdir string) string {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return ""
	}
	host, owner, name, ok := gitForge(raw)
	if !ok {
		return ""
	}
	sub, ok := cleanForgeSubdir(subdir)
	if !ok {
		return ""
	}
	link := "https://" + host + "/" + owner + "/" + name + "/tree/" + sha
	if sub != "" {
		link += "/" + sub
	}
	return link
}

func gitForge(raw string) (host, owner, name string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "file:") || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, ".") {
		return "", "", "", false
	}
	var repoPath string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", "", "", false
		}
		switch u.Scheme {
		case "https", "http", "ssh":
		default:
			return "", "", "", false
		}
		host = u.Hostname()
		repoPath = u.Path
	case strings.HasPrefix(raw, "git@"):
		rest := strings.TrimPrefix(raw, "git@")
		colon := strings.Index(rest, ":")
		if colon <= 0 {
			return "", "", "", false
		}
		host = rest[:colon]
		if host == "" || strings.Contains(host, "/") {
			return "", "", "", false
		}
		repoPath = rest[colon+1:]
	default:
		return "", "", "", false
	}
	repoPath = strings.TrimSuffix(strings.Trim(repoPath, "/"), ".git")
	parts := strings.Split(repoPath, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	if strings.Contains(parts[0], "..") || strings.Contains(parts[1], "..") {
		return "", "", "", false
	}
	return host, parts[0], parts[1], true
}

func cleanForgeSubdir(sub string) (string, bool) {
	sub = strings.Trim(strings.TrimSpace(sub), "/")
	if sub == "" {
		return "", true
	}
	if strings.Contains(sub, "\\") || strings.Contains(sub, "..") || strings.HasPrefix(sub, "/") {
		return "", false
	}
	return sub, true
}
