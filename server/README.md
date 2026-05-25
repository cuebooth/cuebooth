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

See [`../docs/design.md`](../docs/design.md) §3 for the full architecture and the "delegate to Companion unless there's a specific reason not to" principle.

## Status

Not yet scaffolded. The Go module, directory structure under `cmd/` and `internal/`, and config skeleton will be created in Phase 1 (see design doc §5).

Planned layout:

```
server/
├── go.mod
├── cmd/cuebooth-server/main.go
├── internal/
│   ├── companion/    Companion HTTP API client
│   ├── audio/        Mixer OSC client, meters, automation
│   ├── camera/       VISCA velocity PTZ
│   ├── obs/          OBS WebSocket client (video relay)
│   ├── slides/       Slide rule parser and executor
│   ├── hid/          USB HID input (clicker)
│   └── api/          WebSocket API server for clients
└── configs/
    └── cuebooth.toml
```

## Distribution

The server is intended to run as a Windows service on the production PC. Release builds will be packaged as a Windows installer via GitHub Actions (see [`../.github/workflows/README.md`](../.github/workflows/README.md)).
