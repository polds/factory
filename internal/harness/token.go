package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"
)

const (
	// RoleLead may hold roster.dispatch. RoleSpecialist and RoleTool never do.
	RoleOrchestrator = "orchestrator"
	RoleLead         = "lead"
	RoleSpecialist   = "specialist"
	RoleTool         = "tool"

	GrantRosterDispatch = "roster.dispatch"
	GrantInboxRead      = "inbox.read"
	GrantAckWrite       = "ack.write"
	GrantTaskRead       = "task.read"
	GrantTaskWrite      = "task.write"
	GrantCommentWrite   = "comment.write"

	runTokenTTL = 15 * time.Minute
)

// TokenScope is the server authority for one task invocation.
type TokenScope struct {
	FactoryID    string
	TaskID       string
	InvocationID string
	Role         string
	Grants       []string
	BudgetCents  int64
}

// Attenuation narrows a scope. It cannot retarget the task or invocation,
// widen grants, or raise the budget.
type Attenuation struct {
	TaskID       string
	InvocationID string
	Role         string
	Grants       []string
	BudgetCents  int64
}

// RunToken is a short-lived attenuated credential. Formatting, JSON, and slog redact it.
type RunToken struct {
	id           string
	material     string
	factoryID    string
	taskID       string
	invocationID string
	role         string
	grants       map[string]struct{}
	budgetCents  int64
	expires      time.Time
}

// ID is the public ntok_ identifier. It is not the secret.
func (t RunToken) ID() string { return t.id }

// Value is the secret material. Callers must not log it.
func (t RunToken) Value() string { return t.material }

// FactoryID is the factory the token is bound to.
func (t RunToken) FactoryID() string { return t.factoryID }

// TaskID is the only task this token may name.
func (t RunToken) TaskID() string { return t.taskID }

// InvocationID is the only invocation this token may name.
func (t RunToken) InvocationID() string { return t.invocationID }

// Role is orchestrator, lead, specialist, or tool.
func (t RunToken) Role() string { return t.role }

// BudgetCents is the ceiling copied onto the token.
func (t RunToken) BudgetCents() int64 { return t.budgetCents }

// Grants returns a sorted copy of the token's grants.
func (t RunToken) Grants() []string {
	out := make([]string, 0, len(t.grants))
	for name := range t.grants {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// HasGrant reports whether the token holds name.
func (t RunToken) HasGrant(name string) bool {
	_, ok := t.grants[name]
	return ok
}

// Scope returns the authority this token can pass to a child. It is already attenuated.
func (t RunToken) Scope() TokenScope {
	return TokenScope{
		FactoryID:    t.factoryID,
		TaskID:       t.taskID,
		InvocationID: t.invocationID,
		Role:         t.role,
		Grants:       t.Grants(),
		BudgetCents:  t.budgetCents,
	}
}

func (t RunToken) String() string { return "harness.RunToken{id:" + t.id + "}" }

func (t RunToken) GoString() string { return t.String() }

// Format redacts the secret for every verb.
func (t RunToken) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, t.String())
}

// LogValue implements slog.LogValuer.
func (t RunToken) LogValue() slog.Value { return slog.StringValue(t.String()) }

// MarshalJSON writes the public id and grants, never the secret.
func (t RunToken) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID           string   `json:"id"`
		TaskID       string   `json:"taskId"`
		InvocationID string   `json:"invocationId"`
		Role         string   `json:"role"`
		Grants       []string `json:"grants"`
		BudgetCents  int64    `json:"budgetCents"`
	}{
		ID:           t.id,
		TaskID:       t.taskID,
		InvocationID: t.invocationID,
		Role:         t.role,
		Grants:       t.Grants(),
		BudgetCents:  t.budgetCents,
	})
}

// IssueRunToken mints a server token from scope, narrowed by attenuation.
// A specialist or tool token omits roster.dispatch. A child cannot cross
// task or invocation, and cannot raise grants or budget.
func IssueRunToken(ctx context.Context, scope TokenScope, att Attenuation) (RunToken, error) {
	if err := ctx.Err(); err != nil {
		return RunToken{}, err
	}
	if scope.TaskID == "" || scope.InvocationID == "" || scope.FactoryID == "" {
		return RunToken{}, errors.New("harness: run token scope is incomplete")
	}
	taskID := scope.TaskID
	if att.TaskID != "" {
		taskID = att.TaskID
	}
	invocationID := scope.InvocationID
	if att.InvocationID != "" {
		invocationID = att.InvocationID
	}
	if taskID != scope.TaskID || invocationID != scope.InvocationID {
		return RunToken{}, errors.New("harness: run token cannot cross task or invocation")
	}
	role := scope.Role
	if att.Role != "" {
		role = att.Role
	}
	if !knownRole(role) {
		return RunToken{}, fmt.Errorf("harness: run token role %q", role)
	}
	budget := scope.BudgetCents
	if att.BudgetCents != 0 {
		budget = att.BudgetCents
	}
	if budget > scope.BudgetCents {
		return RunToken{}, errors.New("budget_exceeded")
	}
	requested := scope.Grants
	if att.Grants != nil {
		requested = att.Grants
	}
	parent := grantSet(scope.Grants)
	child := map[string]struct{}{}
	for _, name := range requested {
		if name == "" {
			return RunToken{}, errors.New("grant_rejected")
		}
		if _, ok := parent[name]; !ok {
			return RunToken{}, errors.New("grant_rejected")
		}
		child[name] = struct{}{}
	}
	if role == RoleSpecialist || role == RoleTool {
		delete(child, GrantRosterDispatch)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return RunToken{}, fmt.Errorf("harness: run token: %w", err)
	}
	sum := hex.EncodeToString(raw)
	return RunToken{
		id:           "ntok_" + sum[:16],
		material:     sum,
		factoryID:    scope.FactoryID,
		taskID:       taskID,
		invocationID: invocationID,
		role:         role,
		grants:       child,
		budgetCents:  budget,
		expires:      time.Now().Add(runTokenTTL),
	}, nil
}

// Authorize checks grant, task, invocation, and expiry.
func Authorize(tok RunToken, grant, taskID, invocationID string) error {
	if tok.material == "" || tok.id == "" {
		return errors.New("harness: run token is empty")
	}
	if time.Now().After(tok.expires) {
		return errors.New("harness: run token expired")
	}
	if taskID != "" && taskID != tok.taskID {
		return errors.New("harness: run token cannot cross task or invocation")
	}
	if invocationID != "" && invocationID != tok.invocationID {
		return errors.New("harness: run token cannot cross task or invocation")
	}
	if grant != "" && !tok.HasGrant(grant) {
		return errors.New("grant_rejected")
	}
	if (tok.role == RoleSpecialist || tok.role == RoleTool) && grant == GrantRosterDispatch {
		return errors.New("grant_rejected")
	}
	return nil
}

func grantSet(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

func knownRole(role string) bool {
	switch role {
	case RoleOrchestrator, RoleLead, RoleSpecialist, RoleTool:
		return true
	default:
		return false
	}
}
