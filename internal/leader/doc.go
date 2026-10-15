// Package leader elects a single holder for a named lease.
//
// The cron ticker (and any other singleton loop) runs only while it holds
// the lease. A grant lasts LeaseTTL (15s) and the holder renews every
// RenewInterval (5s), so a live process refreshes the lease three times
// before it would expire. The store grants the lease when it is already
// held by this holder or when expires_at < now. A missed renew lets another
// holder take it; the loser stops ticking on its next renew, which is at
// most one RenewInterval after the previous attempt.
//
// T30 binds LeaseStore to the T17 leader_leases row. This package does not
// open Postgres.
package leader
