// Package webui serves the Flutter web client from the same listener as the
// WebSocket API.
//
// It has to be the same listener: /ws compares Origin against Host, so a client
// page served from any other port cannot open a socket to it. Serving the client
// here satisfies that check without weakening it, and means a browser on any
// machine on the network is a working client with nothing installed.
//
// The build output is not committed. `make web` fills dist/ before the binary is
// built; a binary built without it still runs and says so.
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the built client. The all: prefix keeps the committed .gitkeep
// visible to embed, which needs at least one file to match.
//
//go:embed all:dist
var dist embed.FS

// entry is the document a browser navigation is given when the path is not a
// bundled asset, so client-side routing survives a page load on a deep link.
const entry = "index.html"

func assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Only reachable if the embed directive above stops matching, which is a
		// build-time fact rather than a runtime one.
		panic(err)
	}
	return sub
}

// Bundled reports whether a client was built into this binary.
func Bundled() bool {
	return bundledIn(assets())
}

func bundledIn(files fs.FS) bool {
	info, err := fs.Stat(files, entry)
	return err == nil && !info.IsDir()
}

// Handler serves the client, or an explanation if none was bundled.
func Handler() http.Handler {
	if !Bundled() {
		return http.HandlerFunc(serveMissing)
	}
	files := assets()
	tags := etags(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleWith(files, tags, w, r)
	})
}

// etags digests every bundled file once, so responses carry a validator.
//
// The build has no content-hashed filenames, so a URL alone never proves which
// version a cache holds; without this a client re-downloads megabytes on every
// load because there is nothing to revalidate against.
func etags(files fs.FS) map[string]string {
	tags := map[string]string{}
	_ = fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(body)
		tags[name] = `"` + hex.EncodeToString(sum[:16]) + `"`
		return nil
	})
	return tags
}

// handleWith resolves one request against the bundled files. Split from Handler
// so it can be driven against an FS that is not the embedded build.
func handleWith(files fs.FS, tags map[string]string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Clean before trimming, so a path climbing out of the bundle resolves to
	// something inside it rather than reaching the filesystem.
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "." {
		name = entry
	}

	if !serveable(files, name) {
		// A navigation gets the app so a deep link or a refresh lands in it. An
		// asset request does not: answering a missing script with HTML turns a
		// half-staged build into a blank page and a syntax error in the console
		// rather than a 404 naming the file.
		if !acceptsHTML(r) {
			http.NotFound(w, r)
			return
		}
		name = entry
	}

	if tag := tags[name]; tag != "" {
		w.Header().Set("ETag", tag)
	}
	// Revalidate rather than trust age: the filenames carry no version, so a
	// cached copy cannot be shown to match the build being served.
	w.Header().Set("Cache-Control", "no-cache")

	serve(w, r, files, name)
}

// serveable reports whether name is a bundled file this handler will return.
// Directories are excluded — fs.Stat succeeds for them, and reading one fails
// later with an error that reads as though the client itself were unreadable.
// So are dotfiles, which are build leavings rather than part of the app.
func serveable(files fs.FS, name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") {
			return false
		}
	}
	info, err := fs.Stat(files, name)
	return err == nil && !info.IsDir()
}

// acceptsHTML reports whether the request looks like a browser navigation.
//
// A navigation names text/html explicitly; a script, wasm or fetch subresource
// sends */* instead, so */* must not count — treating it as a navigation is
// what hands a missing asset the entry document. An absent header is a bare
// HTTP client with no stated preference, which is likelier to want the page.
func acceptsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return accept == "" || strings.Contains(accept, "text/html")
}

// serve writes one bundled file.
//
// Deliberately not http.FileServer: that redirects a request for index.html
// back to the directory it sits in, which against a rewrite of "/" to
// "index.html" is a redirect loop.
func serve(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
	f, err := files.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, "could not read bundled client", http.StatusInternalServerError)
		return
	}

	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, info.ModTime(), rs)
		return
	}
	// embed.FS files seek, so this is a fallback for an FS that does not.
	body, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "could not read bundled client", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(body))
}

func serveMissing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(missingPage))
}

const missingPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>No client bundled — CueBooth</title>
<style>
  body { font-family: system-ui, sans-serif; background: #11151b; color: #e3e7ec;
         display: flex; min-height: 100vh; margin: 0; align-items: center; justify-content: center; }
  main { max-width: 30rem; padding: 2rem; }
  h1 { font-size: 1.4rem; margin: 0 0 0.75rem; }
  p { color: #9aa4b0; line-height: 1.6; }
  code { background: #212932; padding: 0.15em 0.4em; border-radius: 3px; }
</style>
</head>
<body><main>
<h1>No web client in this build</h1>
<p>The server is running and its WebSocket API is available, but no client was
built into this binary.</p>
<p>Build one with <code>make web</code> in <code>server/</code>, then rebuild
the server. A native client can still connect to this server directly.</p>
</main></body>
</html>
`
