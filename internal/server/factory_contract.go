package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/store"
)

// factoryContract is the applied definition fields a run is allowed to use.
type factoryContract struct {
	Repositories []definition.Repository
	Publish      *definition.Publish
	Evidence     *definition.Evidence
	CeilingCents int64
	HasCeiling   bool
	Rates        json.RawMessage
	Secrets      []string
}

func contractFromTree(tree *definition.Tree) factoryContract {
	if tree == nil {
		return factoryContract{}
	}
	c := factoryContract{
		Repositories: append([]definition.Repository{}, tree.Factory.Repositories...),
		Publish:      tree.Factory.Publish,
		Evidence:     tree.Factory.Evidence,
		Secrets:      append([]string{}, tree.Factory.Secrets...),
	}
	if tree.Factory.Budget != nil && tree.Factory.Budget.CeilingCents > 0 {
		c.CeilingCents = tree.Factory.Budget.CeilingCents
		c.HasCeiling = true
	}
	if tree.Factory.Inference != nil && tree.Factory.Inference.Rates != nil {
		if raw, err := json.Marshal(tree.Factory.Inference.Rates); err == nil {
			c.Rates = raw
		}
	}
	return c
}

func contractFromSpec(raw json.RawMessage) (factoryContract, error) {
	var spec struct {
		Secrets      []string                `json:"secrets"`
		Repositories []definition.Repository `json:"repositories"`
		Publish      *definition.Publish     `json:"publish"`
		Evidence     *definition.Evidence    `json:"evidence"`
		Budget       *definition.Budget      `json:"budget"`
		Rates        json.RawMessage         `json:"rates"`
	}
	if len(raw) == 0 {
		return factoryContract{}, nil
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return factoryContract{}, err
	}
	c := factoryContract{
		Repositories: spec.Repositories,
		Publish:      spec.Publish,
		Evidence:     spec.Evidence,
		Secrets:      spec.Secrets,
		Rates:        spec.Rates,
	}
	if spec.Budget != nil && spec.Budget.CeilingCents > 0 {
		c.CeilingCents = spec.Budget.CeilingCents
		c.HasCeiling = true
	}
	return c, nil
}

func (s *Server) factoryContract(ctx context.Context, factoryID string) (factoryContract, error) {
	snap := s.snapshot()
	if snap.tree != nil && (snap.factory.ID == factoryID || snap.factory.Name == factoryID) {
		return contractFromTree(snap.tree), nil
	}
	if s.store == nil {
		return factoryContract{}, fmt.Errorf("%w: factory contract", store.ErrNotFound)
	}
	raw, err := s.store.DefinitionSpec(ctx, factoryID)
	if err != nil {
		return factoryContract{}, err
	}
	return contractFromSpec(raw)
}

func (c factoryContract) repoNames() map[string]struct{} {
	out := map[string]struct{}{}
	for _, repo := range c.Repositories {
		name := strings.TrimSpace(repo.Owner) + "/" + strings.TrimSpace(repo.Name)
		if repo.Owner == "" || repo.Name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

func (c factoryContract) listsRepo(repo string) bool {
	if len(c.Repositories) == 0 {
		return false
	}
	_, ok := c.repoNames()[repo]
	return ok
}

func (c factoryContract) ratesJSON() (string, error) {
	if len(c.Rates) == 0 || string(c.Rates) == "null" {
		return "", errors.New("inference rates are empty")
	}
	parsed, err := harness.ParseSpendPrice(string(c.Rates))
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func repoSlug(repo *definition.Repository) string {
	if repo == nil {
		return ""
	}
	owner := strings.TrimSpace(repo.Owner)
	name := strings.TrimSpace(repo.Name)
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

func (s *Server) reposAllowed(ctx context.Context, factoryID string, repos []string) error {
	if factoryID == "" {
		return nil
	}
	contract, err := s.factoryContract(ctx, factoryID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if len(contract.Repositories) == 0 {
		return nil
	}
	for _, repo := range repos {
		if !contract.listsRepo(repo) {
			return fmt.Errorf("server: github token: repository %s is not on the factory", repo)
		}
	}
	return nil
}

func (s *Server) roleFilesSeat(ctx context.Context, run store.Run) bool {
	if s.roleFilesForTest {
		return true
	}
	if s.store == nil || run.WorkItemID == "" {
		return false
	}
	item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
	if err != nil || item.InvocationID == "" {
		return false
	}
	policy, err := s.store.GetNativePolicy(ctx, item.InvocationID)
	if err != nil {
		return false
	}
	return policy.BundleMode == definition.BundleModeRoleFiles
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
