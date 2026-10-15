// Command apptoken mints one GitHub App installation token for spike/m4
// MODE=live. The token is the only text on stdout. Errors go to stderr and
// do not include the private key or the token.
package main

import (
	"context"
	"crypto/rsa"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/polds/factory/internal/forge/github"
)

func main() {
	repo := flag.String("repo", "", "owner/name repository")
	flag.Usage = func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(), "Usage: apptoken -repo OWNER/NAME\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 0 || strings.TrimSpace(*repo) == "" {
		flag.Usage()
		os.Exit(2)
	}
	cfg, err := github.ConfigFromEnv()
	if err != nil {
		sayErr("apptoken: github app is not configured")
		os.Exit(1)
	}
	if base := strings.TrimSpace(os.Getenv("FACTORY_GITHUB_API_BASE")); base != "" {
		cfg.BaseURL = base
	}
	token, err := mint(context.Background(), cfg.AppID, cfg.Key, cfg.BaseURL, strings.TrimSpace(*repo))
	if err != nil {
		sayErr("apptoken: installation token request failed")
		os.Exit(1)
	}
	if _, err := fmt.Fprintln(os.Stdout, token); err != nil {
		os.Exit(1)
	}
}

func sayErr(text string) {
	if _, err := fmt.Fprintln(os.Stderr, text); err != nil {
		os.Exit(1)
	}
}

func mint(ctx context.Context, appID int64, key *rsa.PrivateKey, baseURL, repo string) (string, error) {
	client, err := github.New(github.Config{AppID: appID, Key: key, BaseURL: baseURL})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id, err := client.InstallationByRepo(ctx, repo)
	if err != nil {
		return "", err
	}
	tok, err := client.InstallationToken(ctx, id, []string{repo}, github.DefaultPermissions())
	if err != nil {
		return "", err
	}
	if tok.Value() == "" {
		return "", errEmptyToken
	}
	return tok.Value(), nil
}

var errEmptyToken = errors.New("apptoken: empty installation token")
