package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxExprLen bounds a schedule expression. Definitions are small; a longer
// string is rejected so a bad row cannot dominate a tick.
const MaxExprLen = 256

// MinEvery is the shortest @every delay (D9, in-repo cron).
const MinEvery = time.Minute

// ParseError is a rejected schedule expression.
type ParseError struct {
	Expr string
	Msg  string
}

func (e *ParseError) Error() string {
	if e.Expr == "" {
		return "cron: " + e.Msg
	}
	return "cron: " + e.Expr + ": " + e.Msg
}

// Expr is a parsed schedule. The zero Expr is not a valid schedule; use Parse.
type Expr struct {
	every  time.Duration
	minute field
	hour   field
	dom    field
	month  field
	dow    field
}

type field struct {
	// any is true only for a bare "*", which is what Vixie treats as
	// unrestricted. A list or step that happens to cover every value is
	// still restricted.
	any  bool
	bits uint64
}

func (f field) match(v int) bool {
	if v < 0 || v > 63 {
		return false
	}
	return f.bits&(uint64(1)<<uint(v)) != 0
}

func (f *field) set(v int) {
	if v < 0 || v > 63 {
		return
	}
	f.bits |= uint64(1) << uint(v)
}

func (f field) next(from, max int) (int, bool) {
	for v := from; v <= max; v++ {
		if f.match(v) {
			return v, true
		}
	}
	return 0, false
}

var monthNames = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var dowNames = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

// Parse accepts a 5-field UTC cron expression or a descriptor
// (@hourly, @daily, @weekly, @monthly, @every <dur>). @every must be at
// least one minute. Names and descriptors are case-insensitive.
func Parse(expr string) (Expr, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return Expr{}, &ParseError{Msg: "empty expression"}
	}
	if len(expr) > MaxExprLen {
		return Expr{}, &ParseError{Msg: "expression too long"}
	}
	if strings.HasPrefix(expr, "@") {
		return parseDescriptor(expr)
	}
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Expr{}, &ParseError{Expr: expr, Msg: "want 5 fields or a descriptor"}
	}
	var out Expr
	var err error
	if out.minute, err = parseField(parts[0], 0, 59, nil); err != nil {
		return Expr{}, &ParseError{Expr: expr, Msg: "minute: " + err.Error()}
	}
	if out.hour, err = parseField(parts[1], 0, 23, nil); err != nil {
		return Expr{}, &ParseError{Expr: expr, Msg: "hour: " + err.Error()}
	}
	if out.dom, err = parseField(parts[2], 1, 31, nil); err != nil {
		return Expr{}, &ParseError{Expr: expr, Msg: "day of month: " + err.Error()}
	}
	if out.month, err = parseField(parts[3], 1, 12, monthNames); err != nil {
		return Expr{}, &ParseError{Expr: expr, Msg: "month: " + err.Error()}
	}
	if out.dow, err = parseField(parts[4], 0, 7, dowNames); err != nil {
		return Expr{}, &ParseError{Expr: expr, Msg: "day of week: " + err.Error()}
	}
	// 7 and 0 are both Sunday. Fold 7 onto 0 so matching uses Go's Sunday=0.
	if out.dow.bits&(uint64(1)<<7) != 0 {
		out.dow.bits &^= uint64(1) << 7
		out.dow.set(0)
	}
	return out, nil
}

func parseDescriptor(expr string) (Expr, error) {
	parts := strings.Fields(expr)
	switch strings.ToLower(parts[0]) {
	case "@hourly":
		if len(parts) != 1 {
			return Expr{}, &ParseError{Expr: expr, Msg: "@hourly takes no argument"}
		}
		return Parse("0 * * * *")
	case "@daily":
		if len(parts) != 1 {
			return Expr{}, &ParseError{Expr: expr, Msg: "@daily takes no argument"}
		}
		return Parse("0 0 * * *")
	case "@weekly":
		if len(parts) != 1 {
			return Expr{}, &ParseError{Expr: expr, Msg: "@weekly takes no argument"}
		}
		return Parse("0 0 * * 0")
	case "@monthly":
		if len(parts) != 1 {
			return Expr{}, &ParseError{Expr: expr, Msg: "@monthly takes no argument"}
		}
		return Parse("0 0 1 * *")
	case "@every":
		if len(parts) != 2 {
			return Expr{}, &ParseError{Expr: expr, Msg: "@every requires one duration"}
		}
		d, err := time.ParseDuration(strings.ToLower(parts[1]))
		if err != nil {
			return Expr{}, &ParseError{Expr: expr, Msg: "@every: " + err.Error()}
		}
		if d < MinEvery {
			return Expr{}, &ParseError{Expr: expr, Msg: "@every must be at least 1m"}
		}
		return Expr{every: d}, nil
	default:
		return Expr{}, &ParseError{Expr: expr, Msg: "unknown descriptor"}
	}
}

func parseField(raw string, minV, maxV int, names map[string]int) (field, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return field{}, fmt.Errorf("empty")
	}
	var f field
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return field{}, fmt.Errorf("empty list entry")
		}
		if part == "*" {
			f.any = true
			for v := minV; v <= maxV; v++ {
				f.set(v)
			}
			continue
		}
		step := 1
		body := part
		if before, after, ok := strings.Cut(part, "/"); ok {
			body = before
			if after == "" || strings.Contains(after, "/") {
				return field{}, fmt.Errorf("bad step %q", part)
			}
			n, err := strconv.Atoi(after)
			if err != nil || n <= 0 {
				return field{}, fmt.Errorf("bad step %q", part)
			}
			step = n
		}
		start, end, err := fieldRange(body, minV, maxV, names, step)
		if err != nil {
			return field{}, err
		}
		for v := start; v <= end; v += step {
			f.set(v)
		}
	}
	if f.bits == 0 {
		return field{}, fmt.Errorf("matches nothing")
	}
	return f, nil
}

func fieldRange(body string, minV, maxV int, names map[string]int, step int) (int, int, error) {
	if body == "" {
		return 0, 0, fmt.Errorf("missing range")
	}
	if body == "*" {
		return minV, maxV, nil
	}
	if lo, hi, ok := strings.Cut(body, "-"); ok {
		if lo == "" || hi == "" || strings.Contains(hi, "-") {
			return 0, 0, fmt.Errorf("bad range %q", body)
		}
		start, err := parseAtom(lo, names)
		if err != nil {
			return 0, 0, err
		}
		end, err := parseAtom(hi, names)
		if err != nil {
			return 0, 0, err
		}
		if start > end {
			return 0, 0, fmt.Errorf("inverted range %q", body)
		}
		if start < minV || end > maxV {
			return 0, 0, fmt.Errorf("value out of range %d-%d", minV, maxV)
		}
		return start, end, nil
	}
	v, err := parseAtom(body, names)
	if err != nil {
		return 0, 0, err
	}
	if v < minV || v > maxV {
		return 0, 0, fmt.Errorf("value out of range %d-%d", minV, maxV)
	}
	if step == 1 {
		return v, v, nil
	}
	// n/step runs from n through the end of the field.
	return v, maxV, nil
}

func parseAtom(s string, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToUpper(s)]; ok {
			return v, nil
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return n, nil
}

// Next returns the soonest activation strictly after `after`, in UTC.
// A zero time means the expression cannot fire within five years (for
// example day 30 of February).
func (e Expr) Next(after time.Time) time.Time {
	if e.every > 0 {
		return after.UTC().Add(e.every).Truncate(time.Second)
	}
	t := after.UTC()
	t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !e.month.match(int(t.Month())) {
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
			continue
		}
		if !e.dayMatch(t) {
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
			continue
		}
		if !e.hour.match(t.Hour()) {
			hour, ok := e.hour.next(t.Hour()+1, 23)
			if !ok {
				t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
				continue
			}
			t = time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, time.UTC)
			continue
		}
		if !e.minute.match(t.Minute()) {
			minute, ok := e.minute.next(t.Minute()+1, 59)
			if !ok {
				t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC).Add(time.Hour)
				continue
			}
			return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), minute, 0, 0, time.UTC)
		}
		return t
	}
	return time.Time{}
}

func (e Expr) dayMatch(t time.Time) bool {
	domOK := e.dom.match(t.Day())
	dowOK := e.dow.match(int(t.Weekday()))
	switch {
	case e.dom.any && e.dow.any:
		return true
	case e.dom.any:
		return dowOK
	case e.dow.any:
		return domOK
	default:
		return domOK || dowOK
	}
}
