// Package cron parses factory schedule expressions and decides when a
// leader-elected ticker fires them.
//
// Expressions are 5-field UTC cron (minute, hour, day of month, month, day
// of week) plus the descriptors @hourly, @daily, @weekly, @monthly, and
// @every. There is no third-party cron dependency (M4 D9).
//
// Day-of-month and day-of-week follow Vixie semantics: if both fields are
// restricted, a day matches when either field matches. A field is restricted
// when it is not a bare "*".
//
// The ticker fires an overdue schedule once when its latest slot at or
// before now still sits inside the catch-up window (D8, default 1h). Older
// slots in that same gap are not replayed. A latest slot outside the window
// is a single schedule_missed event, and next_fire_at still advances.
// Definition apply updates a cron change in place and replaces a name change.
package cron
