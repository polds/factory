//go:build !console

package console

import (
	"io"
	"net/http"
)

const placeholderPage = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Factory console</title></head>
<body><p>Console not built. Build factoryd with the console tag.</p></body>
</html>
`

func serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.WriteString(w, placeholderPage); err != nil {
		return
	}
}
