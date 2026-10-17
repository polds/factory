package server

import (
	"context"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/store"
)

const factoryEventPage = 500

// FactoryEvents tails the factory log. The SSE id is FactorySeq, the cursor
// assigned after commit. GID on the wire is that cursor. run_events.gid is
// the insert identity and is not a resume cursor: a later commit can hold a
// lower factory_seq than an earlier gid.
func (s *Server) FactoryEvents(ctx context.Context, factory string, from int64) (<-chan api.FactoryEvent, error) {
	fac, err := s.factory(ctx, factory)
	if err != nil {
		return nil, err
	}
	out := make(chan api.FactoryEvent, 16)
	go s.tailFactory(ctx, fac.ID, from, out)
	return out, nil
}

func (s *Server) tailFactory(ctx context.Context, factoryID string, from int64, out chan<- api.FactoryEvent) {
	defer close(out)
	send := func(rows []store.FactoryEvent) bool {
		for _, row := range rows {
			ev := api.FactoryEvent{
				GID:   row.FactorySeq,
				RunID: row.RunID,
				Type:  row.Type,
				Data:  row.Payload,
			}
			select {
			case <-ctx.Done():
				return false
			case out <- ev:
			}
			if row.FactorySeq > from {
				from = row.FactorySeq
			}
		}
		return true
	}
	drain := func() bool {
		for {
			page, err := s.store.FactoryEventsFrom(ctx, factoryID, from, factoryEventPage)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Error("factory events", "err", err)
				}
				return false
			}
			if len(page) == 0 {
				return true
			}
			if !send(page) {
				return false
			}
			if len(page) < factoryEventPage {
				return true
			}
		}
	}
	// Read once before Listen. A commit between that read and Listen is
	// caught by the read immediately after Listen is registered.
	if !drain() {
		return
	}
	notes, err := s.store.Listen(ctx, "run_events")
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("factory events listen", "err", err)
		}
		return
	}
	if !drain() {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-notes:
		case <-tick.C:
		}
		if !drain() {
			return
		}
	}
}
