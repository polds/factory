package cron

import (
	"errors"
	"testing"
	"time"
)

func TestParseRejects(t *testing.T) {
	cases := []string{
		"",
		"*",
		"* * * *",
		"* * * * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * 32 * *",
		"* * * 0 *",
		"* * * 13 *",
		"* * * * 8",
		"* * * JAN-FOO *",
		"*/0 * * * *",
		"5-1 * * * *",
		"@yearly",
		"@every",
		"@every 0s",
		"@every 59s",
		"@every -1m",
		"@every 1h extra",
		"@daily extra",
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			_, err := Parse(expr)
			if err == nil {
				t.Fatal("expected error")
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error type %T", err)
			}
		})
	}
}

func TestParseDescriptors(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"@hourly", "0 * * * *"},
		{"@HOURLY", "0 * * * *"},
		{"@daily", "0 0 * * *"},
		{"@weekly", "0 0 * * 0"},
		{"@monthly", "0 0 1 * *"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Parse(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			want, err := Parse(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("got %#v want %#v", got, want)
			}
		})
	}
}

func TestEveryMinimum(t *testing.T) {
	expr, err := Parse("@every 1m")
	if err != nil {
		t.Fatal(err)
	}
	if expr.every != time.Minute {
		t.Fatalf("every=%s", expr.every)
	}
	if _, err := Parse("@every 1m0s"); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("@every 90s"); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("@every 1H30M"); err != nil {
		t.Fatal(err)
	}
}

func TestNext(t *testing.T) {
	utc := func(y int, m time.Month, d, hh, mm int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, time.UTC)
	}
	cases := []struct {
		expr  string
		after time.Time
		want  time.Time
	}{
		{"* * * * *", utc(2026, 1, 1, 0, 0), utc(2026, 1, 1, 0, 1)},
		{"0 * * * *", utc(2026, 1, 1, 0, 0), utc(2026, 1, 1, 1, 0)},
		{"0 * * * *", utc(2026, 1, 1, 0, 30), utc(2026, 1, 1, 1, 0)},
		{"@hourly", utc(2026, 1, 1, 0, 30), utc(2026, 1, 1, 1, 0)},
		{"30 4 * * *", utc(2026, 1, 1, 4, 30), utc(2026, 1, 2, 4, 30)},
		{"0 0 * * 1", utc(2026, 10, 15, 12, 0), utc(2026, 10, 19, 0, 0)},
		{"0 0 * * MON", utc(2026, 10, 15, 12, 0), utc(2026, 10, 19, 0, 0)},
		{"0 6 * * 1", utc(2026, 10, 18, 0, 0), utc(2026, 10, 19, 6, 0)},
		{"@weekly", utc(2026, 10, 15, 12, 0), utc(2026, 10, 18, 0, 0)},
		{"@daily", utc(2026, 10, 15, 0, 0), utc(2026, 10, 16, 0, 0)},
		{"@monthly", utc(2026, 10, 15, 0, 0), utc(2026, 11, 1, 0, 0)},
		{"*/15 * * * *", utc(2026, 10, 15, 10, 0), utc(2026, 10, 15, 10, 15)},
		{"*/15 * * * *", utc(2026, 10, 15, 10, 14), utc(2026, 10, 15, 10, 15)},
		{"0,30 * * * *", utc(2026, 10, 15, 10, 0), utc(2026, 10, 15, 10, 30)},
		{"0 9 * * 1-5", utc(2026, 10, 16, 17, 0), utc(2026, 10, 19, 9, 0)},
		{"0 0 1 1 *", utc(2026, 6, 1, 0, 0), utc(2027, 1, 1, 0, 0)},
		{"0 0 1 JAN *", utc(2026, 6, 1, 0, 0), utc(2027, 1, 1, 0, 0)},
		{"0 0 1 JAN,JUL *", utc(2026, 2, 1, 0, 0), utc(2026, 7, 1, 0, 0)},
		{"0 12 * JAN-MAR MON", utc(2026, 1, 1, 0, 0), utc(2026, 1, 5, 12, 0)},
		{"0 0 31 * *", utc(2026, 1, 31, 0, 0), utc(2026, 3, 31, 0, 0)},
		{"0 0 29 2 *", utc(2024, 2, 29, 0, 0), utc(2028, 2, 29, 0, 0)},
		{"@every 1h", utc(2026, 10, 15, 10, 0), utc(2026, 10, 15, 11, 0)},
		{"@every 90s", utc(2026, 10, 15, 10, 0), utc(2026, 10, 15, 10, 1).Add(30 * time.Second)},
		{"0 0 1 * MON", utc(2026, 10, 15, 0, 0), utc(2026, 10, 19, 0, 0)},
		{"0 0 1 * MON", utc(2026, 10, 19, 0, 0), utc(2026, 10, 26, 0, 0)},
		{"0 0 1 * MON", utc(2026, 10, 26, 0, 0), utc(2026, 11, 1, 0, 0)},
		{"0 0 1 * MON", utc(2026, 11, 1, 0, 0), utc(2026, 11, 2, 0, 0)},
		{"0 0 * * 7", utc(2026, 10, 15, 0, 0), utc(2026, 10, 18, 0, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			expr, err := Parse(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			got := expr.Next(tc.after)
			if !got.Equal(tc.want) {
				t.Fatalf("Next(%s)=%s want %s", tc.after.Format(time.RFC3339), got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
			if got.Location() != time.UTC {
				t.Fatalf("location %s", got.Location())
			}
			if !got.After(tc.after) {
				t.Fatalf("Next %s is not after %s", got, tc.after)
			}
		})
	}
}

func TestNextUsesUTC(t *testing.T) {
	loc := time.FixedZone("EST", -5*3600)
	after := time.Date(2026, 10, 15, 0, 0, 0, 0, loc) // 05:00 UTC
	expr, err := Parse("0 6 * * *")
	if err != nil {
		t.Fatal(err)
	}
	got := expr.Next(after)
	want := time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}

	every, err := Parse("@every 1h")
	if err != nil {
		t.Fatal(err)
	}
	got = every.Next(after)
	want = time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("@every got %s want %s", got, want)
	}
}

func TestNextStrictlyAfterSubsecond(t *testing.T) {
	after := time.Date(2026, 10, 15, 10, 15, 0, 500, time.UTC)
	expr, err := Parse("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	got := expr.Next(after)
	want := time.Date(2026, 10, 15, 10, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %s", got)
	}
}

func TestNextImpossibleIsZero(t *testing.T) {
	expr, err := Parse("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	got := expr.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if !got.IsZero() {
		t.Fatalf("got %s", got)
	}
}

func TestLatestLongGap(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 34, 56, 0, time.UTC)
	from := now.Add(-400 * 24 * time.Hour)
	expr, err := Parse("* * * * *")
	if err != nil {
		t.Fatal(err)
	}
	slot, ok := expr.latest(from, now)
	if !ok {
		t.Fatal("not found")
	}
	want := time.Date(2026, 10, 15, 12, 34, 0, 0, time.UTC)
	if !slot.Equal(want) {
		t.Fatalf("calendar latest=%s", slot)
	}

	every, err := Parse("@every 1m")
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	slot, ok = every.latest(anchor, now)
	if !ok {
		t.Fatal("every not found")
	}
	want = anchor.Add(now.Sub(anchor).Truncate(time.Minute))
	if !slot.Equal(want) || slot.After(now) {
		t.Fatalf("every latest=%s want %s", slot, want)
	}
}

func TestNextStepsStayOnSchedule(t *testing.T) {
	expr, err := Parse("*/5 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	cur := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := cur.AddDate(0, 0, 2)
	for cur.Before(end) {
		nxt := expr.Next(cur)
		if !nxt.After(cur) {
			t.Fatalf("%s -> %s", cur, nxt)
		}
		if nxt.Minute()%5 != 0 || nxt.Second() != 0 {
			t.Fatalf("off grid %s", nxt)
		}
		cur = nxt
	}
}
