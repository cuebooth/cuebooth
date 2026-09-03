// Package webui serves the Flutter web client from the same listener as the
// WebSocket API.
//
// It has to be the same listener: /ws enforces a same-origin policy, so a
// client page served from any other port cannot open a socket to it. Serving
// the client here satisfies that check without weakening it, and means a
// browser on any machine on the network is a working client with nothing
// installed.
//
// The build output is not committed. `make web` fills dist/ before the binary
// is built; a binary built without it still runs and says so.
package webui

import (
	"bytes"
	"embed"
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

// entry is the file a browser is given for any path that is not a bundled
// asset, so client-side routing survives a page load on a deep link.
const entry = "index.html"

func assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Only reachable if the embed directive above stops matching, which is
		// a build-time fact rather than a runtime one.
		panic(err)
	}
	return sub
}

// Bundled reports whether a client was built into this binary.
func Bundled() bool {
	_, err := fs.Stat(assets(), entry)
	return err == nil
}

// Handler serves the client, or an explanation if none was bundled.
func Handler() http.Handler {
	if !Bundled() {
		return http.HandlerFunc(serveMissing)
	}

	files := assets()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleWith(files, w, r)
	})
}

// handleWith resolves one request against the bundled files. Split from Handler
// so it can be driven against an FS that is not the embedded build.
func handleWith(files fs.FS, w http.ResponseWriter, r *http.Request) {
	// Clean before trimming, so a path climbing out of the bundle resolves to
	// something inside it rather than reaching the filesystem.
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "." {
		name = entry
	}

	if _, err := fs.Stat(files, name); err != nil {
		// Not a bundled asset: hand back the entry document so a deep link or a
		// refresh lands in the app rather than on a 404.
		name = entry
	}

	if name == entry {
		// The entry document names the hashed assets, so a stale copy would pin
		// a browser to a previous build.
		w.Header().Set("Cache-Control", "no-cache")
	}

	serve(w, r, files, name)
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
