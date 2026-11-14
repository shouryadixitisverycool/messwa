package dashboard

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andybalholm/brotli"
)

func TestAssets(t *testing.T) {
	css := ".dual{font-family:Pixel}" + strings.Repeat(".x{color:red}", 50) + "@font-face{src:url(f.woff2)}"
	files := fstest.MapFS{
		"style.css":  {Data: []byte(css)},
		"f.woff2":    {Data: []byte("wOF2 font bytes")},
		"index.html": {Data: []byte(`<link rel="stylesheet" href="style.css"><link rel="preload" href="f.woff2"><a href="sales.html">`)},
	}
	h := assets(files)
	get := func(path string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}

	// The page names each file with its hash; pages themselves aren't renamed.
	page := get("/").Body.String()
	cssURL := regexp.MustCompile(`href="(style\.css\?v=[0-9a-f]+)"`).FindStringSubmatch(page)
	if cssURL == nil || !regexp.MustCompile(`href="f\.woff2\?v=[0-9a-f]+"`).MatchString(page) || !strings.Contains(page, `href="sales.html"`) {
		t.Fatalf("page not versioned: %s", page)
	}
	// The stylesheet's font URL is versioned the same way as the page's preload.
	fontInPage := regexp.MustCompile(`f\.woff2\?v=[0-9a-f]+`).FindString(page)
	if body := get("/style.css").Body.String(); !strings.Contains(body, "url("+fontInPage+")") {
		t.Fatalf("css font url %q, page preloads %q", body, fontInPage)
	}

	// A hashed URL is kept for good; the bare name revalidates.
	res := get("/" + cssURL[1])
	if cc := res.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("hashed url cache-control %q", cc)
	}
	res = get("/style.css")
	tag := res.Header().Get("ETag")
	if res.Code != 200 || tag == "" || res.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("bare url: %d etag %q cache %q", res.Code, tag, res.Header().Get("Cache-Control"))
	}
	if res := get("/style.css?v=stale"); res.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("stale hash cached: %q", res.Header().Get("Cache-Control"))
	}
	if res := get("/style.css", "If-None-Match", tag); res.Code != http.StatusNotModified {
		t.Fatalf("revalidating an unchanged file: %d, want 304", res.Code)
	}

	// Compressed for browsers that say so, and decodes back to the file.
	served := get("/style.css").Body.String()
	res = get("/style.css", "Accept-Encoding", "gzip, deflate, br")
	if res.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("encoding %q, want br", res.Header().Get("Content-Encoding"))
	}
	if b, _ := io.ReadAll(brotli.NewReader(res.Body)); string(b) != served {
		t.Fatalf("br body doesn't decode to the file")
	}
	res = get("/style.css", "Accept-Encoding", "gzip, br;q=0")
	zr, err := gzip.NewReader(bytes.NewReader(res.Body.Bytes()))
	if res.Header().Get("Content-Encoding") != "gzip" || err != nil {
		t.Fatalf("encoding %q, want gzip", res.Header().Get("Content-Encoding"))
	}
	if b, _ := io.ReadAll(zr); string(b) != served {
		t.Fatalf("gzip body doesn't decode to the file")
	}
	// Fonts are compressed already.
	if res := get("/f.woff2", "Accept-Encoding", "br, gzip"); res.Header().Get("Content-Encoding") != "" || res.Header().Get("Content-Type") != "font/woff2" {
		t.Fatalf("font: encoding %q type %q", res.Header().Get("Content-Encoding"), res.Header().Get("Content-Type"))
	}

	// A page that already said what it needs keeps it.
	res = httptest.NewRecorder()
	res.Header().Set("Cache-Control", "no-store")
	h.ServeHTTP(res, httptest.NewRequest("GET", "/", nil))
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control overwritten: %q", got)
	}
	if res := get("/index.html"); res.Code != http.StatusMovedPermanently {
		t.Fatalf("/index.html: %d", res.Code)
	}
	if res := get("/nope.js"); res.Code != 404 {
		t.Fatalf("missing file: %d", res.Code)
	}
}
