package api

import "net/http"

// SchemaCatalog is the optional unauthenticated JSON Schema mount.
// T4 serves the documents from internal/schema. T10's process implements
// SchemaCatalog. SchemaHandler is read once, when NewHandler registers routes.
// A nil handler answers 404 on the schema routes.
type SchemaCatalog interface {
	SchemaHandler() http.Handler
}

// RegisterSchemaRoutes mounts the unauthenticated JSON Schema routes.
// schemas is the T4 handler. A nil schemas answers 404 problem+json.
//
//	GET /api/v1/factory-files/schemas
//	GET /api/v1/factory-files/schemas/{version}
//	GET /api/v1/factory-files/schemas/{version}/{document}
func RegisterSchemaRoutes(mux *http.ServeMux, schemas http.Handler) {
	serve := func(w http.ResponseWriter, r *http.Request) {
		if schemas == nil {
			writeProblem(w, http.StatusNotFound, "not found")
			return
		}
		schemas.ServeHTTP(w, r)
	}
	mux.HandleFunc("GET /api/v1/factory-files/schemas", serve)
	mux.HandleFunc("GET /api/v1/factory-files/schemas/{version}", serve)
	mux.HandleFunc("GET /api/v1/factory-files/schemas/{version}/{document}", serve)
}
