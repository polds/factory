package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCollectKeepsErrorText(t *testing.T) {
	t.Parallel()
	const op = "store: list scorers"
	scan := func(row pgx.Row) (int, error) {
		var n int
		if err := row.Scan(&n); err != nil {
			return 0, err
		}
		return n, nil
	}
	queryErr := errors.New(`relation "scorer_results" does not exist`)
	scanErr := errors.New("scan broke")
	rowsErr := errors.New("connection reset")

	cases := []struct {
		name string
		rows pgx.Rows
		err  error
		want string
	}{
		{name: "query", err: queryErr, want: op + ": " + queryErr.Error()},
		{name: "scan", rows: &fakeRows{next: []bool{true}, scanErr: scanErr}, want: op + ": " + scanErr.Error()},
		{name: "rows", rows: &fakeRows{rowsErr: rowsErr}, want: op + ": " + rowsErr.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := collect(tc.rows, tc.err, op, scan)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error %v want %s", err, tc.want)
			}
		})
	}

	got, err := collect(&fakeRows{next: []bool{true, true}, values: []int{3, 4}}, nil, op, scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("values %v", got)
	}
	empty, err := collect(&fakeRows{}, nil, op, scan)
	if err != nil || len(empty) != 0 || empty == nil {
		t.Fatalf("empty %#v err %v", empty, err)
	}
}

type fakeRows struct {
	next    []bool
	values  []int
	scanErr error
	rowsErr error
	i       int
	closed  bool
}

func (f *fakeRows) Close() { f.closed = true }

func (f *fakeRows) Err() error { return f.rowsErr }

func (f *fakeRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }

func (f *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (f *fakeRows) Next() bool {
	if f.i >= len(f.next) {
		return false
	}
	ok := f.next[f.i]
	f.i++
	return ok
}

func (f *fakeRows) Scan(dest ...any) error {
	if f.scanErr != nil {
		return f.scanErr
	}
	n := f.i - 1
	if n < 0 || n >= len(f.values) {
		return errors.New("scan index")
	}
	ptr, ok := dest[0].(*int)
	if !ok {
		return errors.New("scan type")
	}
	*ptr = f.values[n]
	return nil
}

func (f *fakeRows) Values() ([]any, error) { return nil, nil }

func (f *fakeRows) RawValues() [][]byte { return nil }

func (f *fakeRows) Conn() *pgx.Conn { return nil }
