package server

import (
	"context"
	"errors"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/store"
)

// applyRoleFilesProof marks a bundled native assignment so the harness writes
// the task file from that assignment and refuses an untrusted setup command.
// A legacy array policy and a run with no native policy are unchanged.
func (s *Server) applyRoleFilesProof(ctx context.Context, run store.Run, env map[string]string) error {
	if s.store == nil || run.WorkItemID == "" || env == nil {
		return nil
	}
	item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
	if errors.Is(err, store.ErrNotFound) || item.InvocationID == "" {
		return nil
	}
	if err != nil {
		return err
	}
	policy, err := s.store.GetNativePolicy(ctx, item.InvocationID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if policy.BundleMode != definition.BundleModeRoleFiles {
		return nil
	}
	env[harness.EnvPreHarnessSetup] = harness.PreHarnessSetupDisabled
	env[harness.EnvBundleMode] = definition.BundleModeRoleFiles
	return nil
}
