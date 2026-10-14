package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

func (s *Server) ListFactories(ctx context.Context) ([]api.Factory, error) {
	rows, err := s.store.ListFactories(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.Factory, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAPIFactory(row))
	}
	return out, nil
}

func (s *Server) GetFactory(ctx context.Context, id string) (api.Factory, error) {
	fac, err := s.factory(ctx, id)
	if err != nil {
		return api.Factory{}, err
	}
	return toAPIFactory(fac), nil
}

func (s *Server) ValidateDefinition(ctx context.Context, factoryID string, tarball io.Reader) ([]api.Diagnostic, error) {
	if tarball != nil {
		if _, err := io.Copy(io.Discard, tarball); err != nil {
			return nil, err
		}
	}
	if _, err := s.factory(ctx, factoryID); err != nil {
		return nil, err
	}
	_, diags, err := loadTree(s.cfg.DefinitionPath)
	if len(diags) > 0 {
		return toDiagnostics(diags), nil
	}
	if err != nil {
		return nil, err
	}
	return []api.Diagnostic{}, nil
}

func (s *Server) ApplyDefinition(ctx context.Context, factoryID string) (api.DefinitionRef, []api.Diagnostic, error) {
	fac, err := s.factory(ctx, factoryID)
	if err != nil {
		return api.DefinitionRef{}, nil, err
	}
	tree, diags, err := loadTree(s.cfg.DefinitionPath)
	if tree == nil {
		if len(diags) > 0 {
			return api.DefinitionRef{}, toDiagnostics(diags), nil
		}
		return api.DefinitionRef{}, nil, err
	}
	if len(errorTexts(diags)) > 0 {
		return api.DefinitionRef{}, toDiagnostics(diags), nil
	}
	if tree.Factory.Name != fac.Name {
		return api.DefinitionRef{}, []api.Diagnostic{{
			File:     "factory.yaml",
			Line:     1,
			Severity: definition.SeverityError,
			Message:  "definition name does not match factory",
		}}, nil
	}
	id, err := s.applyTree(ctx, tree)
	if err != nil {
		return api.DefinitionRef{}, toDiagnostics(diags), err
	}
	return api.DefinitionRef{ID: id}, toDiagnostics(diags), nil
}

func (s *Server) ListAgents(ctx context.Context, factoryID string) ([]api.Agent, error) {
	fac, err := s.factory(ctx, factoryID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListAgents(ctx, fac.ID)
	if err != nil {
		return nil, err
	}
	out := make([]api.Agent, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAPIAgent(row))
	}
	return out, nil
}

func (s *Server) StartRun(ctx context.Context, req api.RunRequest) (api.Run, error) {
	fac, err := s.factory(ctx, req.Factory)
	if err != nil {
		return api.Run{}, err
	}
	snap := s.snapshot()
	agentID, bound, err := s.resolveAgent(req.Agent, fac.ID, snap)
	if err != nil {
		return api.Run{}, err
	}
	priority, err := parsePriority(req.Priority)
	if err != nil {
		return api.Run{}, err
	}
	pool := bound.agent.Pool
	if pool == "" {
		pool = s.cfg.WorkerPool
	}
	run, err := s.store.CreateRun(ctx, store.NewRun{
		FactoryID: fac.ID,
		AgentID:   agentID,
		Pool:      pool,
		Priority:  priority,
		Prompt:    req.Prompt,
		Title:     req.Title,
		Repo:      req.Repo,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return api.Run{}, api.ErrNotFound
		}
		return api.Run{}, err
	}
	s.nudge()
	return toAPIRun(run, s.snapshot()), nil
}

func (s *Server) GetRun(ctx context.Context, id string) (api.Run, error) {
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return api.Run{}, api.ErrNotFound
		}
		return api.Run{}, err
	}
	return toAPIRun(run, s.snapshot()), nil
}

func (s *Server) ListRuns(ctx context.Context, f api.RunFilter) ([]api.Run, error) {
	snap := s.snapshot()
	sf := store.RunFilter{Status: f.Status, Limit: 200}
	if f.FactoryID != "" {
		fac, err := s.factory(ctx, f.FactoryID)
		if err != nil {
			return nil, err
		}
		sf.FactoryID = fac.ID
	}
	if f.Agent != "" {
		id, ok := snap.byName[f.Agent]
		if !ok {
			if _, exists := snap.byID[f.Agent]; exists {
				id = f.Agent
			} else {
				return []api.Run{}, nil
			}
		}
		sf.AgentID = id
	}
	rows, err := s.store.ListRuns(ctx, sf)
	if err != nil {
		return nil, err
	}
	out := make([]api.Run, 0, len(rows))
	for _, run := range rows {
		if f.Worker != "" && run.WorkerID != f.Worker {
			continue
		}
		if f.Model != "" {
			bound, ok := snap.byID[run.AgentID]
			if !ok || bound.agent.Model != f.Model {
				continue
			}
		}
		out = append(out, toAPIRun(run, snap))
	}
	return out, nil
}

func (s *Server) CancelRun(ctx context.Context, id string) (api.Run, error) {
	run, err := s.store.RequestCancel(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return api.Run{}, api.ErrNotFound
		}
		if errors.Is(err, store.ErrAlreadyTerminal) {
			return api.Run{}, api.ErrConflict
		}
		return api.Run{}, err
	}
	if isTerminalStatus(run.Status) {
		s.untrackRun(id)
		s.stopGrace(id)
		s.hub.Publish(id)
		return toAPIRun(run, s.snapshot()), nil
	}
	workerID := run.WorkerID
	if workerID == "" {
		workerID = s.workerForRun(id)
	}
	if workerID != "" {
		if err := s.gw.Cancel(ctx, workerID, id); err != nil {
			s.log.Error("cancel", "run_id", id, "err", err)
		}
	}
	s.armGrace(id)
	return toAPIRun(run, s.snapshot()), nil
}

func (s *Server) IngestEvents(ctx context.Context, runID, bearer string, startIdx int, lines [][]byte) error {
	execID, ok, err := s.matchToken(ctx, runID, tokenHash(bearer))
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrUnauthorized
	}
	if len(lines) == 0 {
		return nil
	}
	evs := make([]json.RawMessage, 0, len(lines))
	for _, line := range lines {
		if !json.Valid(line) {
			return errors.New("server: event is not json")
		}
		evs = append(evs, json.RawMessage(append([]byte(nil), line...)))
	}
	if _, err := s.store.AppendEvents(ctx, runID, execID, startIdx, evs); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return api.ErrUnauthorized
		}
		return err
	}
	s.hub.Publish(runID)
	return nil
}

func (s *Server) factory(ctx context.Context, nameOrID string) (store.Factory, error) {
	fac, err := s.store.GetFactory(ctx, nameOrID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Factory{}, api.ErrNotFound
		}
		return store.Factory{}, err
	}
	return fac, nil
}

func (s *Server) resolveAgent(name, factoryID string, snap loaded) (string, boundAgent, error) {
	if snap.factory.ID != factoryID && snap.factory.Name != factoryID {
		return "", boundAgent{}, api.ErrNotFound
	}
	if name == "" {
		if len(snap.byName) != 1 {
			return "", boundAgent{}, fmt.Errorf("server: agent is required")
		}
		for _, id := range snap.byName {
			return id, snap.byID[id], nil
		}
		return "", boundAgent{}, fmt.Errorf("server: agent is required")
	}
	id, ok := snap.byName[name]
	if !ok {
		return "", boundAgent{}, api.ErrNotFound
	}
	bound, ok := snap.byID[id]
	if !ok {
		return "", boundAgent{}, api.ErrNotFound
	}
	return id, bound, nil
}

func parsePriority(name string) (store.Priority, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "interactive":
		return store.PriorityInteractive, nil
	case "pipeline":
		return store.PriorityPipeline, nil
	case "batch":
		return store.PriorityBatch, nil
	default:
		return 0, fmt.Errorf("server: priority %q", name)
	}
}

func priorityName(p store.Priority) string {
	switch p {
	case store.PriorityBatch:
		return "batch"
	case store.PriorityPipeline:
		return "pipeline"
	case store.PriorityInteractive:
		return "interactive"
	default:
		return ""
	}
}

func toAPIFactory(f store.Factory) api.Factory {
	return api.Factory{ID: f.ID, Name: f.Name, Alias: f.Alias}
}

func toAPIAgent(a store.Agent) api.Agent {
	return api.Agent{ID: a.ID, Name: a.Name, FactoryID: a.FactoryID}
}

func toAPIRun(run store.Run, snap loaded) api.Run {
	agent := ""
	if bound, ok := snap.byID[run.AgentID]; ok {
		agent = bound.name
	}
	return api.Run{
		ID:            run.ID,
		Status:        run.Status,
		FactoryID:     run.FactoryID,
		Agent:         agent,
		Title:         run.Title,
		Priority:      priorityName(run.Priority),
		Repo:          run.Repo,
		FailureReason: run.FailureReason,
	}
}
