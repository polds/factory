package server

import (
	"context"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/webhooks"
)

func applyWebhooks(ctx context.Context, s *Server, factoryID string, hooks []definition.Webhook) error {
	specs := make([]webhooks.Spec, 0, len(hooks))
	for _, hook := range hooks {
		specs = append(specs, webhooks.SpecFromDefinition(hook))
	}
	return webhooks.Apply(ctx, s.store, secretSource{s}, factoryID, specs)
}
