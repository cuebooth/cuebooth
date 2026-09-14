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
| client → server WebSocket | `ws://<server>:7878/ws` (`wss://` behind a TLS front) | client dials the server |
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

If you have no Companion install to point at, [§8](#8-dev-stack-the-whole-server-side-on-one-host) starts a real one in a container alongside the server, reachable from a laptop over Tailscale.

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

Be clear about what that check is, though: it compares two headers the requesting page supplies, so it stops an ordinary cross-origin page and not one that has made this server's address into its own name. v1's real boundary is the network. The chat routes, which hand out a credential rather than press a button, additionally check `Host` against the addresses `chat.public_url` names — it takes a list, so a tailnet name, a LAN address and `localhost` can all work. See [protocol.md](protocol.md) §11.

`make web` is optional. A server built without it starts normally, serves everything else, and answers `/` with a page saying no client is bundled — the WebSocket API and any native client are unaffected. The staged build is gitignored; it is a build artifact of `client/`, not source.

**Size.** The bundle is about 40 MB, taking the binary from 7 MB to 48 MB. Nearly all of it — 37 MB — is CanvasKit, which `flutter build web` emits as six wasm builds. A page load fetches one: this build declares the `canvaskit` renderer, so the loader picks `canvaskit/chromium/canvaskit.wasm` (5.8 MB) on a Chromium browser and `canvaskit/canvaskit.wasm` (7.2 MB) elsewhere. The other four are unreachable: the loader only fetches the skwasm and wimp builds when the selected build's renderer is `skwasm`, which this configuration cannot produce, and reaches `experimental_webparagraph` only if `canvasKitVariant` is set by hand. That is 22 MB with their JavaScript and symbol files, tracked in [CB-098](https://github.com/cuebooth/cuebooth/issues/91). A first page load is around 8 MB all told; there is no compression ([CB-099](https://github.com/cuebooth/cuebooth/issues/92)), though a reload revalidates and gets 304s.

`make web` passes `--no-web-resources-cdn`, which is what makes the loader read CanvasKit from here rather than `gstatic.com`; without the flag the whole 37 MB is embedded and never requested.

**HTTPS works, and the server does not do it.** The server terminates no TLS; a reverse proxy in front of it does. The client follows the page: served over `https://` it opens the socket as `wss://`, served over `http://` it uses `ws://`. The *scheme* needs no configuration on either side.

The *address* does, if chat is enabled. `/chat/*` answers only on the addresses `chat.public_url` names, so reaching the server at a new `https://` name that is not listed leaves the button grid working and Chat answering 403. Adding it to the list fixes that much — but the OAuth redirect is built from the **first** entry only, so an appended name still sends Restream back to the old `http://` address when you re-authorize. Put the new name first, and register the matching `<public_url>/chat/auth/callback` on the Restream application, which compares it exactly. See [protocol.md](protocol.md) §11.

The proxy must pass the original `Host` through unmodified — including not adding a port the browser left out, since `/ws` compares the `Origin`'s host *and port* against `Host` ([protocol.md](protocol.md) §1). A proxy that rewrites `Host` to the backend address, or appends `:443` to it, turns every browser client away while native clients keep working. `tailscale serve` passes it through, and issues a real certificate for the tailnet name without exposing anything publicly:

```sh
cd server && make web && make build
./bin/cuebooth-server -config configs/cuebooth.tls-test.toml   # plain HTTP, on 7878
tailscale serve --bg 7878                                      # https://<name>.ts.net → 127.0.0.1:7878
```

`configs/cuebooth.tls-test.toml` is a fixture for exactly this check: no Companion, no presets, no chat, so nothing that could fail for its own reasons is in the picture. The client settles at "Waiting for the Companion surface…", and that is the pass: with no satellite there is no surface layout to draw, and the screen it appears on is one the client only opens after the socket is up and `hello` has arrived.

Open `https://<name>.ts.net` and the client loads and connects; DevTools → Network → WS shows `wss://<name>.ts.net/ws`. A native client has no page to infer from, so reach the same deployment by typing the scheme into the Host field — see [Connecting to the server](#connecting-to-the-server).

Undo it with `tailscale serve reset` when you are done, or the tailnet name keeps answering after the server is gone.

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

The last successful address is remembered and prefilled on the next launch — including a scheme, if you typed one.

The transport is `ws://` unless something says otherwise, which is the usual case: reach the server by LAN IP or over Tailscale, which provides the encrypted link. Two things select `wss://`. A page served over `https://` always uses it, because a browser refuses a cleartext socket from such a page. And on any build, typing a scheme into the Host field — `wss://production-pc.tailnet.ts.net`, or the `https://` URL from your browser's address bar — selects it explicitly, which is how a *native* client reaches a server behind a TLS front. A bare address stays `ws://`.

A `wss://` or `https://` address settles the port too — the one it carries, or 443, which is where a TLS front answers. So `wss://production-pc.tailnet.ts.net` reaches a `tailscale serve` deployment without touching the Port field, and `wss://production-pc.tailnet.ts.net:8443` reaches one on 8443. While an address supplies the port, the Port field is greyed out and says which port is being used instead.

`ws://` and `http://` imply no port of their own: unless the address spells one out, as `ws://192.168.1.50:8080` does, the port stays whatever the Port field holds — this server's port is a deployment choice and never 80. The Port field is otherwise left exactly as you typed it, and what is remembered for next launch is that value — never a port an address implied.

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

### The dev stack's own checks

```sh
scripts/devstack-test.sh
```

Covers the judgements [`devstack.sh`](../scripts/devstack.sh) acts on — whether the pidfile names the server rather than whatever reused its number, whether Companion is answering rather than merely accepting, and what the server log says about the surface. Starts no containers and writes only under its own scratch directory.

---

## 7. A minimal end-to-end run

On a Mac or Windows laptop, to see the control surface working against your real Companion. (To do the same with no hardware and no local Companion install, use the dev stack in [§8](#8-dev-stack-the-whole-server-side-on-one-host) instead.)

1. Enable Companion's Satellite API (§2) and have a page ready to assign.
2. `cd server && cp configs/cuebooth.example.toml configs/cuebooth.toml`, point `[companion]` at your Companion, then `make web && make build` and run `./bin/cuebooth-server -config configs/cuebooth.toml`.
3. In Companion's Surfaces, assign a page to the `cuebooth` surface.
4. Open `http://<server-host>:7878` in a browser. The address is already filled in; press Connect.

Step 4 needs no Flutter toolchain on the machine you are driving from. For a native client instead, skip `make web` and run `cd client && flutter run -d <your-platform>`, connecting to the server's `host:7878`.

The buttons that appear are whatever page Companion has assigned to the surface — discovered live, nothing defined in the client.

---

## 8. Dev stack: the whole server side on one host

`scripts/devstack.sh` runs Companion and cuebooth-server together on a development machine and publishes them on that machine's Tailscale address, so a real client on a laptop can drive the real thing without the production PC.

**Linux only.** It detaches the server with `setsid` and identifies it again by `/proc/<pid>/exe`; it refuses to run without `/proc` rather than mistaking a healthy server for a dead one. `python3` is used to read this host's Tailscale DNS name; without it, `DEVSTACK_HOST` defaults to the bind address instead.

```sh
scripts/devstack.sh up        # start both; prints where to point a client
scripts/devstack.sh status    # what is up, whether it carries a web client, and whether the surface registered
scripts/devstack.sh logs server        # or: logs companion
scripts/devstack.sh restart   # rebuild and restart the server only
scripts/devstack.sh down      # stop both; Companion's config is kept
```

`up` pulls a pinned Companion image, starts it with somewhere to keep its config, generates `.devstack/cuebooth.toml`, builds the server from the working tree, and starts it detached — the stack outlives the shell that launched it. Everything the script itself writes lives under `.devstack/`, which is gitignored.

Where Companion's own config lives depends on the engine. Under **podman** it is bind-mounted to `.devstack/companion/`, readable and backup-able on the host. Under **docker** it is in a managed volume named `cuebooth-devstack-config`, because docker has no equivalent of `--userns keep-id` and the image runs as uid 1000. `down` prints whichever applies.

`up` refuses to start when `.devstack/server.pid` names a live process that is not this stack's server — a server started by hand, or one left by another checkout. Starting anyway would overwrite the only handle to it, and the new server could not bind the port regardless.

`up` leaves a running server and a running Companion alone; it does not rebuild either. After editing server code, use `restart`, which builds first and only stops the running server once the build succeeds. Changing `DEVSTACK_COMPANION_VERSION` does recreate the container — `up` compares the running container's image, not just its name — but changing `DEVSTACK_BIND` does not, since the addresses a container publishes are fixed when it is created; `up` warns and `down` then `up` republishes.

**One-time setup, in Companion's web UI:** build a page of buttons (the built-in `internal` connection gives you page navigation and variable displays with no hardware attached), then assign that page to the `cuebooth` surface under **Surfaces**. That config persists across `down`/`up`.

Companion's config directory is shared across image tags, so switching `DEVSTACK_COMPANION_VERSION` runs a different Companion against the same data. `up` warns when the tag has changed since the last run, and warns harder on a downgrade, because the newer version migrates the directory in place.

`up` reuses a running container only when its image *and* its published ports are the ones this run would ask for. A container created before a port knob changed — or before the Satellite port came off the tailnet — is a mismatch, and `up` says so; `down` then `up` recreates it.

Faster, if you have a `.companionconfig` export from a real installation: drop it on **Import/Export → Import**. Exports back to Companion 2.x are accepted — 3.x upgrades them on the way in — so an old backup still works. Two things to know before importing a production export:

- **It carries credentials.** Module passwords (OBS, for one) travel in the export and are readable in Companion's admin UI, which this stack publishes on your tailnet without authentication. Blank them in the JSON first unless you need them, or expect anyone on the tailnet to be able to read them.
- **Connections will sit in an error state**, because they point at the real deployment's hosts. Buttons still render and presses still route, so the surface is fully exercisable; feedback-driven colours that depend on a live mixer or OBS will not be.

Then, from a laptop on the same tailnet:

```sh
cd client && flutter run -d macos      # or windows, or a device
```

…and connect to the `host:7878` that `status` prints.

Not `-d chrome`: a Flutter dev server serves the page from its own port, and the server's WebSocket refuses a page whose origin is not its own, so the connect attempt gets a 403 whatever address you type. The server's own port is the exception — the page and the socket share it, which is what makes a browser a client at all ([§3.1](#31-bundling-the-web-client-into-the-server)).

**Whether that browser has anything to open is a property of the build, and `status` says which.** `build_server` runs a plain `go build`, which embeds whatever `make web` last staged in `server/internal/webui/dist` — and nothing here stages it, so by default `http://<host>:7878` answers with the page saying no client is bundled. To serve the client from there too:

```sh
make -C server web
scripts/devstack.sh restart      # go build, embedding what make web staged
```

The staged build persists until `make web-clean`, so later `restart`s keep embedding it. `up` and `status` report which this build is, so it is not something to keep track of:

```
client      bundled — open http://<host>:7878 in a browser
client      none — this build has no web client; use a native client
client      unknown — nothing in <log> says
```

The third is a property of the log, not of the build: `.devstack/server.log` spans every run and is never rotated, so an operator who truncates it to read it leaves nothing that says what the running binary carries. A `restart` makes the server report itself again.

Staging is deliberately not automatic. It would put the Flutter SDK on the dependency list of a fixture that otherwise needs only Go, podman and python3, and add about a minute to every `restart` — for a step most runs of this stack do not want.

### What it binds, and what it doesn't

By default nothing is published on `0.0.0.0`.

- Companion's **admin UI** is published on loopback and the address `DEVSTACK_BIND` names (by default this host's Tailscale IPv4).
- Companion's **Satellite port** is published on **loopback only**, whatever `DEVSTACK_BIND` says. The server reaches it over `127.0.0.1`, and nothing off this host needs an endpoint that will hand out the operator's buttons to anyone who sends `ADD-DEVICE`.
- The **CueBooth server** listens on the `DEVSTACK_BIND` address, because `[server] listen` takes a single address.

So `DEVSTACK_BIND` sets two things, and a wildcard there puts **two unauthenticated services on every interface**: Companion's admin UI, and the server's WebSocket API, which has no in-protocol auth in v1 ([protocol.md](protocol.md) §1). `up` says so when you set one. For the admin port a wildcard replaces the loopback publish rather than joining it, since binding `0.0.0.0` over `127.0.0.1` on the same port does not work.

The generated `.devstack/cuebooth.toml` is kept across runs, so anything that changes between runs — a Tailscale address, any of the port knobs — leaves the file describing the previous one. `up` names each setting that disagrees with what this run would have written; `DEVSTACK_REGENERATE=1` rewrites the file. This matters most for the Satellite port: a config still pointing at `16622` sends the server to whatever holds it, which on a machine that already runs Companion is the operator's own.

Nothing needs to be reachable from the public internet. That holds even for the Restream chat authorization (CB-017): the OAuth callback is a redirect the *operator's browser* follows, not a connection Restream makes inbound, so tailnet reachability is enough — no `tailscale funnel`.

### Knobs

| Variable | Default | |
|---|---|---|
| `DEVSTACK_COMPANION_VERSION` | `v3.4.1` | Companion image tag — match the production PC |
| `DEVSTACK_BIND` | this host's Tailscale IPv4 | address for Companion's admin UI and the server's `listen` |
| `DEVSTACK_HOST` | this host's Tailscale DNS name | name printed in connect instructions |
| `DEVSTACK_DIR` | `<repo>/.devstack` | where local state lives |
| `DEVSTACK_ADMIN_PORT` | `8000` | Companion's admin UI — move it if you already run Companion here |
| `DEVSTACK_SAT_PORT` | `16622` | Companion's Satellite port |
| `DEVSTACK_SERVER_PORT` | `7878` | the CueBooth server |
| `DEVSTACK_REGENERATE` | unset | `1` rewrites `.devstack/cuebooth.toml`, discarding edits |
| `CONTAINER_ENGINE` | podman, else docker | |

### What it is not

It is not CI, and it does not replace [`scripts/companion-live-test.sh`](../scripts/companion-live-test.sh) (§6) — that pins protocol behaviour against specific Companion versions and runs headless. This is a fixture for driving the system by hand.

The mixer is out of scope: an XR18 has no emulator worth using, so Phase 2 audio work still needs the hardware in front of you.
