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

---

## 4. Flutter client (`client/`)

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
2. `cd server && cp configs/cuebooth.example.toml configs/cuebooth.toml`, point `[companion]` at your Companion, then `./bin/cuebooth-server -config configs/cuebooth.toml` (after `make build`) or `make run`.
3. In Companion's Surfaces, assign a page to the `cuebooth` surface.
4. `cd client && flutter run -d <your-platform>`, and connect to the server's `host:7878`.

The buttons that appear are whatever page Companion has assigned to the surface — discovered live, nothing defined in the client.

---

## 8. Dev stack: the whole server side on one host

`scripts/devstack.sh` runs Companion and cuebooth-server together on a development machine and publishes them on that machine's Tailscale address, so a real client on a laptop can drive the real thing without the production PC.

**Linux only.** It detaches the server with `setsid` and identifies it again by `/proc/<pid>/exe`; it refuses to run without `/proc` rather than mistaking a healthy server for a dead one. `python3` is used to read this host's Tailscale DNS name; without it, `DEVSTACK_HOST` defaults to the bind address instead.

```sh
scripts/devstack.sh up        # start both; prints where to point a client
scripts/devstack.sh status    # what is up, and whether the surface registered
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

Not `-d chrome`: a Flutter dev server serves the page from its own port, and the server's WebSocket refuses a page whose origin is not its own, so the connect attempt gets a 403 whatever address you type.

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
