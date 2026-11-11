package dreaminglive

import (
	"context"
	"strings"

	"github.com/polds/factory/internal/server"
)

// LsRemote lists refs/heads/<prefix>* using the publisher's git runner.
// header is the Authorization value from GitHTTPAuthHeader, or a deliberate
// wrong form in the offline failure case. It is never placed in argv.
func LsRemote(ctx context.Context, remote, header, prefix string) ([]string, error) {
	stdout, err := server.GitExecHeader(ctx, "", header, remote,
		"ls-remote", remote, "refs/heads/"+prefix+"*")
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		_, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if strings.HasPrefix(name, "refs/heads/"+prefix) {
			refs = append(refs, name)
		}
	}
	return refs, nil
}

// AuthHeader returns the Basic x-access-token header the publisher uses.
func AuthHeader(token string) (string, error) {
	return server.GitHTTPAuthHeader(token)
}
