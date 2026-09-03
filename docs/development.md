# Building & Running

How to build CueBooth from source and run the full stack locally — for development, and for testing a change end-to-end before it ships. Pair it with the [design doc](design.md) for architecture and the [protocol spec](protocol.md) for the wire format.

> **No prebuilt downloads yet.** CI ([`.github/workflows/`](../.github/workflows/README.md)) currently only verifies the build (vet/build/test/analyze); it does not publish runnable artifacts. The release/installer workflows that will attach binaries to a tagged release are tracked in [CB-087](https://github.com/cuebooth/cuebooth/issues/71). Until then, testing on a real machine means building from source as described here.

---

## 1. The moving parts

A running deployment is up to four processes. For the Phase-1 control surface you need the first three:

```
Bitfocus Companion            CueBooth server               Flutter client
(Satellite TCP host     ◄───  (satellite client; also  ───► (renders the surface,
 :16622, HTTP :8000)          serves the client WS :7878)     sends presses)

C# PowerPoint sidecar  ───►  CueBooth server  (named pipe, Windows only — Phase 4)
```

The only wiring constraints:

| Link | Default | Who connects to whom |
|---|---|---|
| server → Companion Satellite API | TCP `16622` | server dials Companion |
| server → Companion HTTP API | `http://localhost:8000` | server calls Companion |
| client → server WebSocket | `ws://<server>:7878/ws` | client dials the server |
| sidecar → server | `\\.\pipe\cuebooth-sidecar` | sidecar writes to the server |

The processes can all run on one machine or be split across the network however you like (e.g. Companion + server on the production PC, client on an iPad over Tailscale). Point a native client at the server's reachable address; a browser client is served by the server itself, so it is already pointed at it ([§3.1](#31-bundling-the-web-client-into-the-server)).

---

## 2. Bitfocus Companion (the button source)

The native button grid is rendered from [Companion](https://bitfocus.io/companion)'s own Satellite surface — there is **nothing to configure client-side** (see [protocol.md §10](protocol.md)). To feed it:

1. In Companion, **enable the Satellite API**. It listens on **TCP port 16622** by default. (Companion 3.5+ also exposes a WebSocket variant on 16623; CueBooth uses TCP, so 16622 is all you need. Exact menu placement varies by Companion version — look under Settings.)
2. Start the server (below). It registers a surface named by `device_id` (default `cuebooth`).
3. In Companion's **Surfaces**, assign a page to that surface. Companion renders that page's buttons to bitmaps and streams them to the client; taps route back as button presses.

The server *declares* the surface shape to Companion from the `[companion.satellite]` config (`rows`/`cols`/`bitmap_size`); the default `4×8 / 72px` registers a Stream Deck XL–shaped surface.

If you want to exercise the server and client **without** Companion, you only need something that speaks the Satellite host side of the protocol on 16622 — a small mock is enough for development.

---

## 3. CueBooth server (`server/`)

**Prerequisites:** Go **1.26+**. A C compiler (gcc/clang) is needed only to run the race detector (`go test -race`); a plain build or run does not need CGO.

From `server/`:

```sh
# Run against the example config (good for development)
make run

# Or build a native binary
make build              # → bin/cuebooth-server
./bin/cuebooth-server -config configs/cuebooth.toml

# Cross-compile the Windows production binary
make build-windows      # → bin/cuebooth-server.exe
```

`make run` uses `configs/cuebooth.example.toml`. For your own setup, copy it and edit:

```sh
cp configs/cuebooth.example.toml configs/cuebooth.toml
```

Key fields (the file documents the rest):

- `[server] listen` — `0.0.0.0:7878` to accept connections from the LAN/Tailscale; `127.0.0.1:7878` for local-only.
- `[companion] base_url` — Companion's HTTP API, usually `http://localhost:8000`.
- `[companion.satellite] addr` — `localhost:16622`, or `"off"` to disable the surface; plus `device_id`, `rows`, `cols`, `bitmap_size`.

The server binds the client WebSocket on `[server] listen` at path `/ws` (`/ws/meters` is reserved for later phases). The default config path when no `-config` is given is `configs/cuebooth.toml`.

### 3.1 Bundling the web client into the server

The server can carry the Flutter web client and serve it at `/` on the same port, so a browser on any machine is a working client with nothing installed:

```sh
cd server
make web      # flutter build web, staged into internal/webui/dist
make build    # go build, embedding whatever make web staged
```

Then open `http://<server-host>:7878`. The connect screen prefills the address the page came from, so there is nothing to type.

**It has to be the same port.** `/ws` enforces a same-origin policy (coder/websocket's default compares `Origin` against `Host`), so a client page served from anywhere else is refused with a 403. Serving it here satisfies that check rather than weakening it — worth keeping, since v1 has no in-protocol auth ([protocol.md](protocol.md) §1).

`make web` is optional. A server built without it starts normally, serves everything else, and answers `/` with a page saying no client is bundled — the WebSocket API and any native client are unaffected. The staged build is gitignored; it is a build artifact of `client/`, not source.

**Size.** The bundle is about 40 MB, taking the binary from 7 MB to 48 MB. Nearly all of it — 37 MB — is CanvasKit, which `flutter build web` emits as six wasm builds. A page load fetches one: this build declares the `canvaskit` renderer, so the loader picks `canvaskit/chromium/canvaskit.wasm` (5.8 MB) on a Chromium browser and `canvaskit/canvaskit.wasm` (7.2 MB) elsewhere. The other four cannot be selected without setting `canvasKitVariant` by hand — 22 MB with their JavaScript and symbol files, tracked in [CB-098](https://github.com/cuebooth/cuebooth/issues/91). A first page load is around 8 MB all told; there is no compression ([CB-099](https://github.com/cuebooth/cuebooth/issues/92)), though a reload revalidates and gets 304s.

`make web` passes `--no-web-resources-cdn`, which is what makes the loader read CanvasKit from here rather than `gstatic.com`; without the flag the whole 37 MB is embedded and never requested.

**Plain HTTP only.** The client builds a `ws://` URL unconditionally, so a page served over HTTPS loads and then cannot connect — the browser blocks the socket as mixed content, and the connect screen's origin prefill will have filled in port 443. Serve this over plain HTTP and reach it by LAN IP or tailnet address. [CB-097](https://github.com/cuebooth/cuebooth/issues/90) tracks choosing the scheme from the page.

---

## 4. Flutter client (`client/`)

> **You may not need any of this.** The server can carry the web client and serve it from its own port — open `http://<server>:7878` in a browser and you have a working client with nothing installed and no Flutter toolchain. See [§3.1](#31-bundling-the-web-client-into-the-server). The rest of this section is for building a *native* client, or for developing the client itself.

**Prerequisites (all platforms):** the Flutter SDK (Dart **3.11+**, which Flutter 3.41 onward provides), then the platform-specific toolchain below. After installing the toolchain, fetch dependencies once:

```sh
cd client
flutter pub get
```

Run `flutter doctor` to confirm your target platform shows no outstanding issues. Then follow the section for your platform.

### macOS

1. Install Xcode (from the App Store) and its command-line tools: `xcode-select --install`.
2. Install CocoaPods: `sudo gem install cocoapods`.
3. Run:

   ```sh
   flutter run -d macos
   ```

### Windows

1. Install Visual Studio (Community is fine) with the **"Desktop development with C++"** workload.
2. Run:

   ```sh
   flutter run -d windows
   ```

### Linux

1. Install the GTK build toolchain (Debian/Ubuntu):

   ```sh
   sudo apt-get install ninja-build cmake clang libgtk-3-dev pkg-config
   ```

2. Run:

   ```sh
   flutter run -d linux
   ```

### Web (any OS)

> **This cannot reach the server.** `flutter run -d chrome` serves the page from its own port, and `/ws` refuses a page whose origin is not the server's (see [§3.1](#31-bundling-the-web-client-into-the-server)) — the connect attempt gets a 403 whatever address you type. Use it for UI work with no server, and use `make web` in `server/` to run the web client against a real server.

1. Install Chrome or Chromium.
2. Run:

   ```sh
   flutter run -d chrome        # or: -d web-server, then open the printed URL
   ```

### iPad / iPhone / Android

1. Connect the device (or start a simulator/emulator) and trust the host if prompted; iOS additionally needs Xcode set up as for macOS.
2. List devices, then run on one:

   ```sh
   flutter devices
   flutter run -d <device-id>
   ```

   Distribution is via the App Store / Play Store or a development sideload.

### Connecting to the server

On the **Connect** screen, enter the server's `host:port`:

- everything on one machine → `127.0.0.1` / `7878`
- client on a separate device → the server's LAN or Tailscale IP / `7878`

The last successful address is remembered and prefilled on the next launch. The transport is cleartext `ws://` for v1 (reach the server by LAN IP or over Tailscale, which provides the encrypted link).

**In a browser, only one address works**: the one the page was served from, which is already prefilled. `/ws` compares the request's `Origin` against its `Host`, so a page served from `192.168.1.50:7878` gets a 403 if you point it at the same server's tailnet address instead. The fields are editable because the same screen runs on native builds, where any reachable address is fine.

---

## 5. C# PowerPoint sidecar (`sidecar/`) — Windows only

Not required for the control surface; it drives slide automation in Phase 4. PowerPoint COM interop is Windows-only.

**Prerequisites:** .NET SDK (targets `net10.0-windows`), PowerPoint installed.

```sh
dotnet restore
dotnet build -c Release
dotnet run            # connects to the server's named pipe
```

See [`sidecar/README.md`](../sidecar/README.md) for details.

---

## 6. Running the tests

```sh
# Server
cd server
go test ./...                       # unit/integration tests
CGO_ENABLED=1 go test -race ./...   # with the race detector (needs a C compiler)
go vet ./...

# Client
cd client
flutter analyze
flutter test
```

These mirror what CI runs on every push (see [`.github/workflows/README.md`](../.github/workflows/README.md)).

### Against a real Companion

The suites above use an in-memory fake for Companion. To exercise the Satellite client against a **real** Companion, run:

```sh
scripts/companion-live-test.sh v3.4.1      # or v5.0.3
```

It starts that Companion version in a container (podman or docker, whichever is present), waits for the Satellite port, runs the integration test, and removes the container. Useful variants:

```sh
COMPANION_KEEP=1 scripts/companion-live-test.sh v5.0.3        # leave it running to poke at the admin UI
COMPANION_SATELLITE_ADDR=127.0.0.1:16622 scripts/companion-live-test.sh   # use a Companion you already run
```

CI runs this same script against both versions (see the workflows README), so a local run and a CI run do the same thing. The test is skipped by a plain `go test ./...` unless `COMPANION_SATELLITE_ADDR` is set, which keeps the normal suite hermetic.

---

## 7. A minimal end-to-end run

On a Mac or Windows laptop, to see the control surface working against your real Companion:

1. Enable Companion's Satellite API (§2) and have a page ready to assign.
2. `cd server && cp configs/cuebooth.example.toml configs/cuebooth.toml`, point `[companion]` at your Companion, then `make web && make build` and run `./bin/cuebooth-server -config configs/cuebooth.toml`.
3. In Companion's Surfaces, assign a page to the `cuebooth` surface.
4. Open `http://<server-host>:7878` in a browser. The address is already filled in; press Connect.

Step 4 needs no Flutter toolchain on the machine you are driving from. For a native client instead, skip `make web` and run `cd client && flutter run -d <your-platform>`, connecting to the server's `host:7878`.

The buttons that appear are whatever page Companion has assigned to the surface — discovered live, nothing defined in the client.
