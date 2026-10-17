// Package console serves the factory web console.
//
// Handler sends /api/ and /api/** to the wrapped API handler. Hashed static
// files are served with an immutable cache. Every other path serves the
// console index so the client router can handle it.
//
// With the console build tag the files come from the embedded Vite build.
// Without the tag, Handler still returns 200 and a short placeholder page,
// so go build and go test do not need Node.
package console

import (
	"net/http"
	"path"
	"strings"
)

// Handler routes console and API requests.
// api receives every method under /api. A nil api answers 500 on those paths.
func Handler(api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleaned := cleanPath(r.URL.Path)
		if isAPI(cleaned) {
			if api == nil {
				http.Error(w, "missing api handler", http.StatusInternalServerError)
				return
			}
			if r.URL.Path != cleaned {
				forward := r.Clone(r.Context())
				forward.URL.Path = cleaned
				forward.URL.RawPath = ""
				r = forward
			}
			api.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		serve(w, r)
	})
}

func cleanPath(raw string) string {
	return path.Clean("/" + strings.TrimPrefix(raw, "/"))
}

func isAPI(cleaned string) bool {
	return cleaned == "/api" || strings.HasPrefix(cleaned, "/api/")
}
