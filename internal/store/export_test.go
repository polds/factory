package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// MigrationFS is the embedded SQL migrations, visible to store_test.
var MigrationFS = migrationFS

// AppendLedgerTxForTest runs one ledger append inside the caller's transaction.
func AppendLedgerTxForTest(ctx context.Context, tx pgx.Tx, invocationID string, entry LedgerEntry) (LedgerRecord, error) {
	return appendLedgerTx(ctx, tx, invocationID, entry)
}
