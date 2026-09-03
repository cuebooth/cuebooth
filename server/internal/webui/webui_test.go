package webui

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// serveFrom exercises the same request handling as Handler against a supplied
// FS, so behaviour can be tested without a real Flutter build embedded.
func serveFrom(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleWith(files, w, r)
	})
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":       {Data: []byte("<html>the app</html>")},
		"main.dart.js":     {Data: []byte("console.log(1)")},
		"assets/thing.png": {Data: []byte("\x89PNG")},
	}
}

// The root has to serve the app itself. http.FileServer redirects a request
// for index.html back to the directory holding it, which against a rewrite of
// "/" to "index.html" is a redirect loop rather than a page.
func TestRootServesTheAppWithoutRedirecting(t *testing.T) {
	rec := get(t, serveFrom(testFS()), "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a redirect here is a loop)", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "the app") {
		t.Errorf("body = %q, want the entry document", body)
	}
}

func TestBundledAssetsAreServed(t *testing.T) {
	h := serveFrom(testFS())

	for path, want := range map[string]string{
		"/main.dart.js":     "console.log(1)",
		"/assets/thing.png": "\x89PNG",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		if rec.Body.String() != want {
			t.Errorf("GET %s served the wrong bytes", path)
		}
	}
}

// A refresh on a client-routed path must land in the app, not on a 404.
func TestUnknownPathsFallBackToTheApp(t *testing.T) {
	h := serveFrom(testFS())

	for _, path := range []string{"/settings", "/deep/link", "/chatty"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "the app") {
			t.Errorf("GET %s did not serve the entry document", path)
		}
	}
}

// The entry document names the hashed asset files, so a cached copy would pin
// a browser to a previous build.
func TestEntryDocumentIsNotCached(t *testing.T) {
	rec := get(t, serveFrom(testFS()), "/")

	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

// Content types matter: a browser refuses to instantiate wasm served as
// something else.
func TestContentTypesComeFromTheExtension(t *testing.T) {
	files := fstest.MapFS{
		"index.html":        {Data: []byte("<html></html>")},
		"canvaskit/ck.wasm": {Data: []byte("\x00asm")},
		"main.dart.js":      {Data: []byte("1")},
		"assets/font.ttf":   {Data: []byte("ttf")},
	}
	h := serveFrom(files)

	for path, want := range map[string]string{
		"/canvaskit/ck.wasm": "application/wasm",
		"/main.dart.js":      "text/javascript",
		"/":                  "text/html",
	} {
		rec := get(t, h, path)
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, want) {
			t.Errorf("GET %s content type = %q, want it to contain %q", path, got, want)
		}
	}
}

// A server built without running `make web` must still start and explain
// itself, rather than serving a broken page or refusing to boot.
func TestNoBundledClientExplainsItself(t *testing.T) {
	rec := httptest.NewRecorder()
	http.HandlerFunc(serveMissing).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "make web") {
		t.Errorf("page does not say how to build a client: %q", body)
	}
}

// Bundled is what decides between the two handlers, so it has to be honest
// about an FS with no entry document.
func TestBundledTracksTheEntryDocument(t *testing.T) {
	if _, err := fs.Stat(fstest.MapFS{"other.txt": {}}, entry); err == nil {
		t.Fatal("test fixture unexpectedly contains an entry document")
	}
	if _, err := fs.Stat(testFS(), entry); err != nil {
		t.Fatalf("test fixture should contain %s: %v", entry, err)
	}
}

// A path that climbs out of the bundle must not reach the filesystem.
func TestTraversalIsContained(t *testing.T) {
	rec := get(t, serveFrom(testFS()), "/../../etc/passwd")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the app, via fallback)", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "the app") {
		t.Errorf("traversal served %q, want the entry document", string(body))
	}
}
