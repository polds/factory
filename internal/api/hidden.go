package api

import (
	"fmt"
	"io"
	"log/slog"
)

// hidden is a credential a backend may store beside a response value.
// Printing, logging, and JSON encoding it yield a placeholder, and the
// response fields that use it also set json:"-" so the key is absent.
type hidden string

func (hidden) String() string { return "[redacted]" }

func (hidden) GoString() string { return `"[redacted]"` }

func (hidden) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted]")
}

func (hidden) MarshalJSON() ([]byte, error) {
	return []byte("null"), nil
}

func (hidden) LogValue() slog.Value {
	return slog.StringValue("[redacted]")
}
