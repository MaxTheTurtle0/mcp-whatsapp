package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestFetchNamesByContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("jpegdata"))
		case "/sniff":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(pngBytes)
		case "/a/report.pdf":
			_, _ = w.Write([]byte("%PDF-1.4"))
		case "/big":
			_, _ = w.Write(make([]byte, 2048))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	f := Fetcher{AllowPrivate: true}
	for _, tc := range []struct{ path, filename, want string }{
		{"/img", "", "img.jpg"},
		{"/sniff", "", "sniff.png"},
		{"/a/report.pdf", "", "report.pdf"},
		{"/img", "../../photo", "photo.jpg"},
		{"/a/report.pdf", "Q3 numbers.pdf", "Q3 numbers.pdf"},
	} {
		p, cleanup, err := f.Fetch(context.Background(), srv.URL+tc.path, root, tc.filename)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if filepath.Base(p) != tc.want {
			t.Errorf("%s: name %q, want %q", tc.path, filepath.Base(p), tc.want)
		}
		if !strings.HasPrefix(p, root+string(filepath.Separator)) {
			t.Errorf("%s: %q escaped root", tc.path, p)
		}
		cleanup()
		if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
			t.Errorf("cleanup left %s", filepath.Dir(p))
		}
	}
	if _, _, err := f.Fetch(context.Background(), srv.URL+"/missing", root, ""); err == nil {
		t.Error("404 should fail")
	}
	small := Fetcher{AllowPrivate: true, MaxBytes: 1024}
	if _, _, err := small.Fetch(context.Background(), srv.URL+"/big", root, ""); err == nil {
		t.Error("oversize should fail")
	}
}

func TestFetchBlocksLocalAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(pngBytes) }))
	defer srv.Close()
	for _, u := range []string{srv.URL + "/x.png", "http://localhost:" + strings.Split(srv.URL, ":")[2] + "/x.png", "file:///etc/passwd", "ftp://example.com/a"} {
		if _, _, err := (Fetcher{}).Fetch(context.Background(), u, t.TempDir(), ""); err == nil {
			t.Errorf("%s: expected refusal", u)
		}
	}
}
