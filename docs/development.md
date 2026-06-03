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

The processes can all run on one machine or be split across the network however you like (e.g. Companion + server on the production PC, client on an iPad over Tailscale). Wherever the client runs, point it at the server's reachable address.

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

---

## 4. Flutter client (`client/`)

**Prerequisites:** Flutter SDK (Dart **3.12+**), plus the desktop/mobile toolchain for your target platform.

| Target | Toolchain to install | Run |
|---|---|---|
| macOS | Xcode + CocoaPods | `flutter run -d macos` |
| Windows | Visual Studio + "Desktop development with C++" | `flutter run -d windows` |
| Linux | `ninja-build cmake clang libgtk-3-dev pkg-config` | `flutter run -d linux` |
| Web | Chrome / Chromium | `flutter run -d chrome` |
| iPad / iPhone / Android | platform store or sideload | `flutter run -d <device>` |

From `client/`:

```sh
flutter pub get
flutter run -d <target>
```

On the **Connect** screen, enter the server's `host:port`:

- everything on one machine → `127.0.0.1` / `7878`
- client on a separate device → the server's LAN or Tailscale IP / `7878`

The last successful address is remembered and prefilled on the next launch. The transport is cleartext `ws://` for v1 (reach the server by LAN IP or over Tailscale, which provides the encrypted link).

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

---

## 7. A minimal end-to-end run

On a Mac or Windows laptop, to see the control surface working against your real Companion:

1. Enable Companion's Satellite API (§2) and have a page ready to assign.
2. `cd server && cp configs/cuebooth.example.toml configs/cuebooth.toml`, point `[companion]` at your Companion, then `./bin/cuebooth-server -config configs/cuebooth.toml` (after `make build`) or `make run`.
3. In Companion's Surfaces, assign a page to the `cuebooth` surface.
4. `cd client && flutter run -d <your-platform>`, and connect to the server's `host:7878`.

The buttons that appear are whatever page Companion has assigned to the surface — discovered live, nothing defined in the client.
