package store

import (
	"context"

	"github.com/polds/factory/internal/cron"
)

// scheduleDeleter is cron.Catalog's DeleteSchedule method.
// List, upsert, and replace stay on Store's signatures: they return
// store.Schedule or take a factory id, and cron.Catalog uses cron.Schedule.
type scheduleDeleter interface {
	DeleteSchedule(ctx context.Context, factoryID, automation, name string) error
}

var (
	_ scheduleDeleter = (*Store)(nil)
	_ scheduleDeleter = cron.Catalog(nil)
)
