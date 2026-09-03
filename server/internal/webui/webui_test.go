package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// serveFrom exercises the same chain as Handler against a supplied FS, so
// behaviour can be tested without a real Flutter build embedded.
func serveFrom(files fs.FS) http.Handler { return handlerFrom(files) }

// noClient is what Handler builds when `make web` was never run.
func noClient() http.Handler { return handlerFrom(fstest.MapFS{}) }

// navigate requests path the way a browser navigating to it does.
func navigate(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// fetch requests path the way a script or wasm subresource does.
func fetch(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Sec-Fetch-Dest", "script")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte("<html>the app</html>")},
		"main.dart.js":      {Data: []byte("console.log(1)")},
		"canvaskit/ck.wasm": {Data: []byte("\x00asm")},
		"assets/thing.png":  {Data: []byte("\x89PNG")},
		".last_build_id":    {Data: []byte("abc123")},
	}
}

// The root has to serve the app itself. http.FileServer redirects a request for
// index.html back to the directory holding it, which against a rewrite of "/"
// to "index.html" is a redirect loop rather than a page.
func TestRootServesTheAppWithoutRedirecting(t *testing.T) {
	rec := navigate(t, serveFrom(testFS()), "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a redirect here is a loop)", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "the app") {
		t.Errorf("body = %q, want the entry document", body)
	}
}

// The root is the app whoever asks for it. A client that states no preference
// for HTML — curl, a health check, anything sending */* — must still get the
// page rather than a 404, since there is no other document the root could mean.
func TestRootServesTheAppRegardlessOfAccept(t *testing.T) {
	h := serveFrom(testFS())

	for _, path := range []string{"/", "//", "/."} {
		rec := fetch(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "the app") {
			t.Errorf("GET %s did not serve the entry document", path)
		}
	}
}

func TestBundledAssetsAreServed(t *testing.T) {
	h := serveFrom(testFS())

	for path, want := range map[string]string{
		"/main.dart.js":      "console.log(1)",
		"/assets/thing.png":  "\x89PNG",
		"/canvaskit/ck.wasm": "\x00asm",
	} {
		rec := fetch(t, h, path)
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
func TestNavigationsFallBackToTheApp(t *testing.T) {
	h := serveFrom(testFS())

	for _, path := range []string{"/settings", "/deep/link", "/chatty"} {
		rec := navigate(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "the app") {
			t.Errorf("GET %s did not serve the entry document", path)
		}
	}
}

// A missing asset must not be answered with the app. A half-staged build would
// otherwise return HTML for a script, which a browser reports as a syntax error
// on a blank page rather than as the missing file it is.
func TestMissingAssetsAreNotAnsweredWithHTML(t *testing.T) {
	h := serveFrom(testFS())

	for _, path := range []string{"/missing.js", "/missing.wasm", "/assets/gone.png"} {
		rec := fetch(t, h, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "the app") {
			t.Errorf("GET %s was answered with the entry document", path)
		}
	}
}

// fs.Stat succeeds for a directory, so without an explicit check the handler
// reaches a read that fails with an error reading as though the bundled client
// itself were unreadable.
func TestDirectoriesDoNotProduceAServerError(t *testing.T) {
	h := serveFrom(testFS())

	for _, path := range []string{"/assets", "/assets/", "/canvaskit"} {
		rec := navigate(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (the app, via fallback)", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "could not read") {
			t.Errorf("GET %s reported the client as unreadable", path)
		}
	}
}

// Build leavings are not part of the app and should not be reachable.
func TestDotfilesAreNotServed(t *testing.T) {
	rec := fetch(t, serveFrom(testFS()), "/.last_build_id")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "abc123") {
		t.Error("a dotfile's contents were served")
	}
}

// The filenames carry no version, so a cached copy cannot be shown to match the
// build being served without revalidating against a validator.
func TestResponsesCarryAValidatorAndRevalidate(t *testing.T) {
	h := serveFrom(testFS())

	rec := fetch(t, h, "/main.dart.js")
	tag := rec.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag; every load re-downloads the whole client")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}

	// A client presenting the validator gets a 304 rather than the bytes again.
	req := httptest.NewRequest(http.MethodGet, "/main.dart.js", nil)
	req.Header.Set("If-None-Match", tag)
	again := httptest.NewRecorder()
	h.ServeHTTP(again, req)

	if again.Code != http.StatusNotModified {
		t.Errorf("revalidation status = %d, want 304", again.Code)
	}
	if again.Body.Len() != 0 {
		t.Errorf("304 carried %d bytes of body", again.Body.Len())
	}
}

// Different content must not share a validator, or a stale asset survives a
// rebuild.
func TestValidatorsFollowContent(t *testing.T) {
	one := etags(fstest.MapFS{"a.js": {Data: []byte("first")}})["a.js"]
	two := etags(fstest.MapFS{"a.js": {Data: []byte("second")}})["a.js"]

	if one == "" || two == "" {
		t.Fatal("etags produced no validator")
	}
	if one == two {
		t.Error("different content produced the same ETag")
	}
}

// Content types matter: a browser refuses to instantiate wasm served as
// something else.
func TestContentTypesComeFromTheExtension(t *testing.T) {
	h := serveFrom(testFS())

	for path, want := range map[string]string{
		"/canvaskit/ck.wasm": "application/wasm",
		"/main.dart.js":      "text/javascript",
	} {
		rec := fetch(t, h, path)
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, want) {
			t.Errorf("GET %s content type = %q, want it to contain %q", path, got, want)
		}
	}
	if got := navigate(t, h, "/").Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Errorf("GET / content type = %q, want text/html", got)
	}
}

// Nothing behind this handler takes a write, so a method that implies one
// should be refused rather than quietly answered with the app — and the answer
// must not depend on whether a client happens to be bundled.
func TestNonReadMethodsAreRefused(t *testing.T) {
	for name, h := range map[string]http.Handler{
		"bundled":   serveFrom(testFS()),
		"no client": noClient(),
	} {
		t.Run(name, func(t *testing.T) {
			for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(method, "/", nil))
				if rec.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s / = %d, want 405", method, rec.Code)
				}
				if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
					t.Errorf("%s / Allow = %q, want \"GET, HEAD\"", method, got)
				}
			}
		})
	}
}

// The page is same-origin with /ws, which admits a page whose Origin equals its
// Host — so a page that could be framed is a page that could be clicked through
// into an API with no auth of its own.
func TestTheAppCannotBeFramed(t *testing.T) {
	for name, h := range map[string]http.Handler{
		"bundled":   serveFrom(testFS()),
		"no client": noClient(),
	} {
		t.Run(name, func(t *testing.T) {
			rec := navigate(t, h, "/")
			if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options = %q, want DENY", got)
			}
			if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
				t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'none'", got)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
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
	if !strings.Contains(rec.Body.String(), "make web") {
		t.Errorf("page does not say how to build a client: %q", rec.Body.String())
	}
}

// bundledIn is the single branch deciding between serving the app and serving
// the no-client page, so it has to be exercised rather than described.
func TestBundledDetection(t *testing.T) {
	cases := []struct {
		name  string
		files fs.FS
		want  bool
	}{
		{"a staged build", testFS(), true},
		{"only a placeholder", fstest.MapFS{".gitkeep": {Data: []byte("x")}}, false},
		{"empty", fstest.MapFS{}, false},
		{"assets but no entry", fstest.MapFS{"main.dart.js": {Data: []byte("1")}}, false},
		// A directory named index.html would satisfy a bare existence check and
		// then fail on read.
		{"a directory where the entry should be", fstest.MapFS{
			"index.html/inner.txt": {Data: []byte("x")},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bundledIn(tc.files); got != tc.want {
				t.Errorf("bundledIn = %v, want %v", got, tc.want)
			}
		})
	}
}

// A build with nothing staged answers with the explanation, whatever is staged
// in the checkout the tests happen to run in.
func TestWithoutAStagedBuildTheExplanationIsServed(t *testing.T) {
	rec := navigate(t, noClient(), "/")

	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "make web") {
		t.Errorf("status = %d, body = %q; want the no-client page", rec.Code, rec.Body.String())
	}
}

// A path that climbs out of the bundle must not reach the filesystem.
func TestTraversalIsContained(t *testing.T) {
	h := serveFrom(testFS())

	for _, path := range []string{"/../../etc/passwd", "/%2e%2e/%2e%2e/etc/passwd", "//etc/passwd"} {
		rec := navigate(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (the app, via fallback)", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "the app") {
			t.Errorf("GET %s served something other than the entry document", path)
		}
	}
}
