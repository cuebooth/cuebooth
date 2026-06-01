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

Phase 0 — foundation, documentation, and project scaffolding. The design is complete and the server, client, and sidecar skeletons are landing now; feature implementation (Phase 1 onward) has not yet begun. See [`docs/design.md`](docs/design.md) for the full architecture and phased plan.

## Repository Layout

```
cuebooth/
├── docs/                    Design and (eventually) sample configuration
├── server/                  Go server (orchestration + automation)
├── client/                  Flutter app (cross-platform control surface)
├── sidecar/                 C# PowerPoint COM monitor
└── .github/workflows/       CI: build server, client, sidecar, and Windows installers
```

## Distribution

Every CueBooth component that runs on Windows is intended to ship as a real Windows installer (built automatically by GitHub Actions on release), not a loose binary drop. The production PC is typically operated by people who don't want to manage services from a PowerShell prompt.

## Documentation

- [Design](docs/design.md) — architecture, technology choices, phased plan
- [Operator runbook](docs/runbook.md) — pre-event setup → going live → teardown, as a template
- [Slide Rules](docs/slide-rules.md) — authoring `@cuebooth` rules in slide notes
- [WebSocket protocol](docs/protocol.md) — client/server wire spec
- [Sample deployment](docs/sample-deployment.md) — worked end-to-end example tying the docs together

Sample configuration files will be added as the implementation phases land.

## License

[MIT](LICENSE)
