# CueBooth Server

The Go orchestration daemon that runs on the production PC.

## Role

The server is the brain of CueBooth. It does **not** reimplement protocol-level hardware control for most devices — it delegates to [Bitfocus Companion](https://bitfocus.io/companion) for that. The server's primary responsibilities are:

- **Automation:** Execute slide-driven rules, audio automation, pre/post-event sequences.
- **Orchestration:** Coordinate actions across Companion HTTP, direct OSC, and direct VISCA into unified workflows.
- **Client API:** Serve a WebSocket API that the Flutter client connects to.
- **Direct hardware (where Companion is inadequate):**
  - OSC to the mixer for real-time meters, fader drag, and audio automation.
  - VISCA to PTZ cameras for velocity-based joystick control.
  - OBS WebSocket for video preview relay.
  - Raw USB HID for the slide clicker.

See [`../docs/design.md`](../docs/design.md) §3 for the full architecture and the "delegate to Companion unless there's a specific reason not to" principle. The client ↔ server wire protocol is specified in [`../docs/protocol.md`](../docs/protocol.md).

## Status

Phase 1 in progress. The Go module, directory layout, and a Windows-service-capable entrypoint are in place (Phase 0). Implemented so far:

- `internal/config` (CB-011) — config loader with the preset-mapping schema.
- `internal/companion` (CB-010) — Companion HTTP API client.
- `internal/api` (CB-012) — WebSocket API server: client connections, command routing, state broadcast, the reserved `/ws/meters` endpoint, ping/pong keepalive, and graceful shutdown.
- `internal/state` (CB-013) — authoritative state store with monotonic revisions and sparse deltas, plus a pluggable poller for background sources.

The entrypoint now wires these together: it loads config, builds the Companion client, and serves the API until stopped. The remaining `internal/` packages (`audio`, `camera`, `obs`, `slides`, `hid`) are documented stubs whose implementations land in later phases (see design doc §5).

Layout:

```
server/
├── go.mod
├── Makefile
├── cmd/cuebooth-server/
│   ├── main.go              Entrypoint + flag/config wiring
│   ├── service_windows.go   Windows service wrapper
│   └── service_other.go     Non-Windows build (run in foreground)
├── internal/
│   ├── config/       TOML config loader and schema
│   ├── companion/    Companion HTTP API client
│   ├── audio/        Mixer OSC client, meters, automation
│   ├── camera/       VISCA velocity PTZ
│   ├── obs/          OBS WebSocket client (video relay)
│   ├── slides/       Slide rule parser and executor
│   ├── hid/          USB HID input (clicker)
│   ├── api/          WebSocket API server for clients
│   └── state/        Authoritative state store + aggregation
└── configs/
    └── cuebooth.example.toml   Copy to cuebooth.toml and edit
```

## Building & running

`make run` builds and runs against the example config; `make build` produces `bin/cuebooth-server`, and `make build-windows` cross-compiles the Windows binary. See [`../docs/development.md`](../docs/development.md) for prerequisites, configuration, and running the full stack.

## Distribution

The server is intended to run as a Windows service on the production PC. Release builds will be packaged as a Windows installer via GitHub Actions (see [`../.github/workflows/README.md`](../.github/workflows/README.md)).
