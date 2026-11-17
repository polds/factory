package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

// FactoryKeyLookup resolves a factory-scoped API key. The deployment admin
// key is not this lookup.
type FactoryKeyLookup interface {
	FactoryIDForKey(ctx context.Context, token string) (string, bool, error)
}

// FactoryIDResolver maps a factory id or name to the factory id.
type FactoryIDResolver interface {
	ResolveFactoryID(ctx context.Context, nameOrID string) (string, error)
}

// ApprovalFactory is the factory that owns an approval.
type ApprovalFactory interface {
	ApprovalFactory(ctx context.Context, approvalID string) (string, error)
}

// DeliveryFactory is the factory that owns a delivery.
type DeliveryFactory interface {
	DeliveryFactory(ctx context.Context, deliveryID string) (string, error)
}

type scopeActor struct {
	admin     bool
	factoryID string
}

type scopeActorKey struct{}

func contextWithActor(ctx context.Context, actor scopeActor) context.Context {
	return context.WithValue(ctx, scopeActorKey{}, actor)
}

func actorFrom(ctx context.Context) (scopeActor, bool) {
	actor, ok := ctx.Value(scopeActorKey{}).(scopeActor)
	return actor, ok
}

func (h *handler) authenticate(r *http.Request) (scopeActor, bool) {
	tok, ok := bearerToken(r)
	if !ok {
		return scopeActor{}, false
	}
	if h.apiKey != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(h.apiKey)) == 1 {
		return scopeActor{admin: true}, true
	}
	keys, ok := h.backend.(FactoryKeyLookup)
	if !ok {
		return scopeActor{}, false
	}
	id, found, err := keys.FactoryIDForKey(r.Context(), tok)
	if err != nil || !found || id == "" {
		return scopeActor{}, false
	}
	return scopeActor{factoryID: id}, true
}

func (h *handler) permitScoped(w http.ResponseWriter, r *http.Request, actor scopeActor) bool {
	path := r.URL.Path
	if r.Method == http.MethodPut && strings.Contains(path, "/definition") {
		writeProblem(w, http.StatusForbidden, "forbidden")
		return false
	}
	if r.Method == http.MethodPost && strings.HasSuffix(path, "/definition:apply") {
		writeProblem(w, http.StatusForbidden, "forbidden")
		return false
	}
	if r.Method == http.MethodPost && strings.HasSuffix(path, "/definition:validate") {
		if h.pathFactory(r) != actor.factoryID {
			writeProblem(w, http.StatusForbidden, "forbidden")
			return false
		}
		return true
	}
	if r.PathValue("uid") != "" && h.pathFactory(r) != actor.factoryID {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return false
		}
		writeProblem(w, http.StatusForbidden, "forbidden")
		return false
	}
	return true
}

func (h *handler) pathFactory(r *http.Request) string {
	uid := strings.TrimSpace(r.PathValue("uid"))
	if uid == "" {
		uid = strings.TrimSpace(r.PathValue("name"))
	}
	return h.canonicalFactory(r, uid)
}

func (h *handler) canonicalFactory(r *http.Request, nameOrID string) string {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return ""
	}
	if res, ok := h.backend.(FactoryIDResolver); ok {
		id, err := res.ResolveFactoryID(r.Context(), nameOrID)
		if err == nil && id != "" {
			return id
		}
	}
	return nameOrID
}

func (h *handler) rejectForeign(w http.ResponseWriter, r *http.Request, factoryID string) bool {
	actor, ok := actorFrom(r.Context())
	if !ok || actor.admin {
		return false
	}
	if h.canonicalFactory(r, factoryID) != actor.factoryID {
		writeProblem(w, http.StatusForbidden, "forbidden")
		return true
	}
	return false
}

func (h *handler) hideForeign(w http.ResponseWriter, r *http.Request, factoryID string) bool {
	actor, ok := actorFrom(r.Context())
	if !ok || actor.admin {
		return false
	}
	if factoryID == "" || h.canonicalFactory(r, factoryID) != actor.factoryID {
		w.WriteHeader(http.StatusNotFound)
		return true
	}
	return false
}

// scopeQuery forces a list filter to the scoped factory. A query that names
// another factory is 403. Admin callers get forced=false and keep their query.
func (h *handler) scopeQuery(w http.ResponseWriter, r *http.Request, query string) (forced string, ok bool) {
	actor, limited := actorFrom(r.Context())
	if !limited || actor.admin {
		return "", true
	}
	named := strings.TrimSpace(r.URL.Query().Get(query))
	if named != "" && h.canonicalFactory(r, named) != actor.factoryID {
		writeProblem(w, http.StatusForbidden, "forbidden")
		return "", false
	}
	return actor.factoryID, true
}
