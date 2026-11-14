package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

// revalidate makes browsers check each static file on every load instead of
// guessing how long to keep it. Without this a browser can pair a fresh
// sales.css with a style.css from before a rule moved between them. The ETag
// is a hash of the embedded file, so an unchanged file costs a 304.
func revalidate(files fs.FS, h http.Handler) http.Handler {
	tags := map[string]string{}
	fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(files, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		tags["/"+p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	if t, ok := tags["/index.html"]; ok {
		tags["/"] = t
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t, ok := tags[r.URL.Path]; ok {
			w.Header().Set("ETag", t)
		}
		// Pages that vary by user already say no-store.
		if w.Header().Get("Cache-Control") == "" && !strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.ServeHTTP(w, r)
	})
}
