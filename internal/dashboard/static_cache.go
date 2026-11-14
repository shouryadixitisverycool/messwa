package dashboard

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
)

// speculationRules tells Chrome to fetch a page as soon as the pointer
// rests on a link to it, so the click only waits on what the page's
// scripts ask for. It's named in a Speculation-Rules header because the
// CSP allows no inline scripts.
const speculationRules = "speculation-rules.json"

// asset is one embedded file, ready to send: its bytes as served, the same
// compressed with gzip and brotli (nil when compressing doesn't pay), and a
// tag that changes with the bytes.
type asset struct {
	body, gz, br []byte
	ctype, tag   string
}

// Pages and stylesheets name the files they load. At startup each such name
// gets "?v=" and the file's hash appended, so a file's URL changes exactly
// when its bytes do and browsers may keep it for good.
var (
	htmlRef = regexp.MustCompile(`((?:href|src)=")([a-z0-9._-]+\.(?:css|js|svg|woff2))(")`)
	cssRef  = regexp.MustCompile(`(url\()([a-z0-9._-]+\.(?:woff2|svg))(\))`)
)

// assets serves the embedded files. A request whose ?v= matches the file's
// hash is cached for a year without checking back; anything else, pages
// included, is revalidated on every load, which an unchanged file answers
// with a 304. Every file goes out compressed when the browser accepts it.
func assets(files fs.FS) http.Handler {
	all := loadAssets(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/index.html" {
			http.Redirect(w, r, "./", http.StatusMovedPermanently)
			return
		}
		if strings.HasSuffix(p, "/") {
			p += "index.html"
		}
		a, ok := all[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("ETag", a.tag)
		h.Set("Vary", "Accept-Encoding")
		if v := r.URL.Query().Get("v"); v != "" && `"`+v+`"` == a.tag {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else if h.Get("Cache-Control") == "" {
			// Pages that vary by user already said what they need.
			h.Set("Cache-Control", "no-cache")
		}
		if match(r.Header.Get("If-None-Match"), a.tag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Type", a.ctype)
		body := a.body
		switch enc := r.Header.Get("Accept-Encoding"); {
		case a.br != nil && accepts(enc, "br"):
			body = a.br
			h.Set("Content-Encoding", "br")
		case a.gz != nil && accepts(enc, "gzip"):
			body = a.gz
			h.Set("Content-Encoding", "gzip")
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method != http.MethodHead {
			w.Write(body)
		}
	})
}

func loadAssets(files fs.FS) map[string]*asset {
	raw := map[string][]byte{}
	fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(files, p)
		if err == nil {
			raw[p] = b
		}
		return err
	})
	hashes := map[string]string{}
	hash := func(b []byte) string {
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:6])
	}
	// Fonts and images first, then stylesheets naming them, then scripts,
	// then pages naming all of those.
	stage := func(p string) int {
		switch path.Ext(p) {
		case ".css":
			return 1
		case ".html":
			return 2
		}
		return 0
	}
	version := func(re *regexp.Regexp, b []byte) []byte {
		return re.ReplaceAllFunc(b, func(m []byte) []byte {
			s := re.FindSubmatch(m)
			h, ok := hashes[string(s[2])]
			if !ok {
				return m
			}
			return []byte(string(s[1]) + string(s[2]) + "?v=" + h + string(s[3]))
		})
	}
	out := map[string]*asset{}
	for st := 0; st <= 2; st++ {
		for p, b := range raw {
			if stage(p) != st {
				continue
			}
			switch st {
			case 1:
				b = version(cssRef, b)
			case 2:
				b = version(htmlRef, b)
			}
			hashes[p] = hash(b)
			out["/"+p] = newAsset(p, b, hashes[p])
		}
	}
	return out
}

func newAsset(name string, b []byte, h string) *asset {
	a := &asset{body: b, tag: `"` + h + `"`, ctype: mime.TypeByExtension(path.Ext(name))}
	if name == speculationRules {
		a.ctype = "application/speculationrules+json"
	}
	if a.ctype == "" {
		a.ctype = http.DetectContentType(b)
	}
	// woff2 and png are compressed already.
	if ext := path.Ext(name); ext == ".woff2" || ext == ".png" {
		return a
	}
	var buf bytes.Buffer
	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	gw.Write(b)
	gw.Close()
	if buf.Len() < len(b)*9/10 {
		a.gz = bytes.Clone(buf.Bytes())
	}
	buf.Reset()
	bw := brotli.NewWriterLevel(&buf, brotli.BestCompression)
	bw.Write(b)
	bw.Close()
	if buf.Len() < len(b)*9/10 {
		a.br = bytes.Clone(buf.Bytes())
	}
	return a
}

// match reports whether an If-None-Match header names tag.
func match(header, tag string) bool {
	for t := range strings.SplitSeq(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == tag || t == "*" {
			return true
		}
	}
	return false
}

// accepts reports whether an Accept-Encoding header allows enc.
func accepts(header, enc string) bool {
	for part := range strings.SplitSeq(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.TrimSpace(name) != enc {
			continue
		}
		q := strings.TrimSpace(params)
		return !(strings.HasPrefix(q, "q=0") && strings.Trim(q[2:], "0.") == "")
	}
	return false
}
