package secrets

import (
	"fmt"
	"log/slog"
)

// Handle is resolved secret material for one injection. It must never leak via fmt or logs.
type Handle struct {
	name  string
	value string
}

// Name is the allowlisted secret name. Safe to log.
func (h Handle) Name() string { return h.name }

// Value returns the secret bytes. Callers inject into env; do not log the return value.
func (h Handle) Value() string { return h.value }

func (h Handle) String() string { return "secrets.Handle{name:" + h.name + "}" }

func (h Handle) GoString() string { return h.String() }

// Format implements fmt.Formatter. Every verb redacts material.
func (h Handle) Format(f fmt.State, verb rune) {
	_, _ = fmt.Fprintf(f, "secrets.Handle{name:%s}", h.name)
}

// LogValue implements slog.LogValuer.
func (h Handle) LogValue() slog.Value {
	return slog.StringValue(h.String())
}

func newHandle(name, value string) Handle {
	return Handle{name: name, value: value}
}
