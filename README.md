# CueBooth

A unified automation and control surface for live-event video streaming.

Live events that combine a video switcher, an audio mixer, a PTZ camera or two, presentation slides, and a remote operator workflow almost always end up driven by a fragile stack of tools wired together — OBS, mixer apps, slide controllers, browser tabs, key-remapping scripts, a vendor utility per device. CueBooth replaces that stack with:

- A **Go server** on the production PC that orchestrates everything.
- A **Flutter client** (iPad, iPhone, Android, desktop, web) — a single control surface usable from anywhere on the network or over Tailscale.
- A small **C# sidecar** that watches PowerPoint via COM events.
- **Slide-driven automation** — slide notes declare desired camera, audio, and scene state, applied immediately or on operator confirm.
- **Direct OSC / VISCA / OBS-WS** only where existing tooling (Bitfocus Companion) is inadequate: real-time audio meters, velocity-based PTZ, video preview relay.

A core goal is that a basic event can be run by a non-technical operator with just a slide clicker, while an experienced operator retains full manual control of everything.

## Origin

CueBooth was started to replace the manual A/V workflow for a hybrid in-person and livestreamed Sunday worship service. The architecture and feature set are not worship-specific — anything from a theater production to a school assembly to a community broadcast shares the same fundamental control-surface needs. If you are running similar live events and want to use CueBooth, you should be able to substitute your own mixer channels, camera presets, and OBS scenes via configuration without touching the underlying control plane.

## Status

**Phase 1 — the Companion control surface — works.** The server registers with Bitfocus Companion as a Satellite surface, streams the rendered button grid to the Flutter client over a WebSocket, and routes presses back. Companion's own page navigation comes with it, so a surface that links to other surfaces behaves as it does on hardware. The server also carries the web client and serves it at `/`, so a browser on any machine on the network is a working client with nothing installed.

It also surfaces the stream's chat. That renders inside the app on iPad, iPhone, Android and macOS; on Windows, Linux and Web it opens in the system browser instead, because Flutter endorses no webview implementation for those platforms. Chat is read-only: the platform exposes no way to send, so replying still means the platform's own app.

The client arranges what it shows as panes rather than as separate screens. Each is either pinned — holding a region of the layout, resized by dragging its divider — or unpinned, waiting behind a tab on its edge and travelling back over the layout when summoned. Every pane can be unpinned, so the operator chooses what stays in view for the whole event. The arrangement is kept per device.

CI exercises the Satellite integration against real Companion releases. None of it has yet been run against a full production rig.

Phases 2 onward — direct OSC audio control and metering, VISCA camera, the slide engine, HID input, video preview — are not implemented; `server/internal/{audio,camera,obs,slides,hid}` are package stubs, and the C# sidecar does not yet drive anything. There are no prebuilt downloads: [building from source](docs/development.md) is the only way to run it, and release packaging is tracked in [CB-087](https://github.com/cuebooth/cuebooth/issues/71).

See [`docs/design.md`](docs/design.md) for the full architecture and phased plan.

## Repository Layout

```
cuebooth/
├── docs/                    Design and (eventually) sample configuration
├── server/                  Go server (orchestration + automation)
├── client/                  Flutter app (cross-platform control surface)
├── sidecar/                 C# PowerPoint COM monitor
└── .github/workflows/       CI: vet/build/test the server, analyze/test the client, build the sidecar
```

## Distribution

Every CueBooth component that runs on Windows is intended to ship as a real Windows installer (built automatically by GitHub Actions on release), not a loose binary drop. The production PC is typically operated by people who don't want to manage services from a PowerShell prompt.

## Documentation

- [Building & running](docs/development.md) — build from source and run the full stack locally
- [Design](docs/design.md) — architecture, technology choices, phased plan
- [Operator runbook](docs/runbook.md) — pre-event setup → going live → teardown, as a template
- [Slide Rules](docs/slide-rules.md) — authoring `@cuebooth` rules in slide notes
- [WebSocket protocol](docs/protocol.md) — client/server wire spec
- [Sample deployment](docs/sample-deployment.md) — worked end-to-end example tying the docs together

A documented example configuration lives at [`server/configs/cuebooth.example.toml`](server/configs/cuebooth.example.toml); sections for phases that aren't implemented yet are sketched there as comments.

## License

[MIT](LICENSE)
