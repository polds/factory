// Package tasks is the native-task entry point M9-N calls.
// Create records a task. It does not claim an activation or start an execution.
package tasks

import (
	"context"

	"github.com/polds/factory/internal/store"
)

// Create inserts one native task through the store. ParentTaskID is a native
// parent, not runs.parent_run_id. TaskRef on the result is the Factory-side id.
func Create(ctx context.Context, st *store.Store, in store.NativeTaskInput) (store.NativeTask, error) {
	return st.InsertNativeTask(ctx, in)
}
