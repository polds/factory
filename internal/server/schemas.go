package server

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/polds/factory/internal/schema"
)

// SchemaHandler is the unauthenticated JSON Schema mount. Nil when the
// schema directory cannot be found; the API then answers 404.
func (s *Server) SchemaHandler() http.Handler {
	return s.schemas
}

func schemaHandler(explicit string) http.Handler {
	dir := findSchemaDir(explicit)
	if dir == "" {
		return nil
	}
	return schema.NewHandler(os.DirFS(dir))
}

// findSchemaDir returns the schemas directory (the parent of v1alpha1).
// explicit may be that directory or a repo root that contains schemas/.
func findSchemaDir(explicit string) string {
	if dir := schemaDir(explicit); dir != "" {
		return dir
	}
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	for _, start := range starts {
		dir := start
		for range 8 {
			if found := schemaDir(dir); found != "" {
				return found
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}

func schemaDir(path string) string {
	if path == "" {
		return ""
	}
	if hasFactorySchema(path) {
		return path
	}
	nested := filepath.Join(path, "schemas")
	if hasFactorySchema(nested) {
		return nested
	}
	return ""
}

func hasFactorySchema(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "v1alpha1", "factory.schema.json"))
	return err == nil && !st.IsDir()
}
