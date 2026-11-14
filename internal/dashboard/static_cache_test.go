package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestRevalidate(t *testing.T) {
	files := fstest.MapFS{"style.css": {Data: []byte(".dual{}")}}
	h := revalidate(files, http.FileServerFS(files))

	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest("GET", "/style.css", nil))
	tag := res.Header().Get("ETag")
	if res.Code != 200 || tag == "" || res.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("first load: %d etag %q cache %q", res.Code, tag, res.Header().Get("Cache-Control"))
	}

	req := httptest.NewRequest("GET", "/style.css", nil)
	req.Header.Set("If-None-Match", tag)
	res = httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusNotModified {
		t.Fatalf("revalidating an unchanged file: %d, want 304", res.Code)
	}

	// A page that already said no-store keeps it.
	res = httptest.NewRecorder()
	res.Header().Set("Cache-Control", "no-store")
	h.ServeHTTP(res, httptest.NewRequest("GET", "/style.css", nil))
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control overwritten: %q", got)
	}
}
