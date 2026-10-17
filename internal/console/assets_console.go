//go:build console

package console

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// The Vite build writes web/console/dist. npm run build copies that tree
// here because go:embed cannot name a parent directory or follow a symlink.

//go:embed all:dist
var embedded embed.FS

func serve(w http.ResponseWriter, r *http.Request) {
	root, err := fs.Sub(embedded, "dist")
	if err != nil {
		http.Error(w, "console assets missing", http.StatusInternalServerError)
		return
	}
	rel := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if rel == "" || rel == "." || rel == "index.html" {
		writeIndex(w, r, root)
		return
	}
	data, err := fs.ReadFile(root, rel)
	if err != nil {
		writeIndex(w, r, root)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, path.Base(rel), time.Time{}, bytes.NewReader(data))
}

func writeIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		http.Error(w, "console index missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
}
