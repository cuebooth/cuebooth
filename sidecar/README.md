# CueBooth PowerPoint Sidecar

A small C# (.NET) process that watches PowerPoint via COM events and forwards slide-change events plus slide metadata to the Go server.

## Role

PowerPoint COM interop is much easier from .NET than from Go. Rather than embedding COM handling in the server, this sidecar isolates it:

- Subscribes to PowerPoint COM events (no polling) to detect slide changes.
- Reads slide notes/comments where `@cuebooth` rules are authored.
- Forwards events to the server over a local IPC channel (named pipe or localhost WebSocket — TBD).
- If PowerPoint is ever replaced (e.g., by Keynote, Google Slides, or a CueBooth-native slide app), only this sidecar needs to be replaced.

See [`../docs/design.md`](../docs/design.md) §3.3 ("PowerPoint Monitor") for the full design and §4 (Phase 4 — Slide Engine).

## Status

Not yet scaffolded. Phase 4 of the design will build the COM-event-based implementation from scratch.

Planned layout:

```
sidecar/
├── PptMonitor.csproj
├── Program.cs
└── (additional files as needed)
```

## Distribution

The sidecar runs on the same Windows production PC as the server. Release builds will be packaged as a Windows installer via GitHub Actions (see [`../.github/workflows/README.md`](../.github/workflows/README.md)) — likely bundled with the server installer so a single install brings both up together.
