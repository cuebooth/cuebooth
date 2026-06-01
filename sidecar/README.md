# CueBooth PowerPoint Sidecar

A small C# (.NET) process that watches PowerPoint via COM events and forwards slide-change events plus slide metadata to the Go server.

## Role

PowerPoint COM interop is much easier from .NET than from Go. Rather than embedding COM handling in the server, this sidecar isolates it:

- Subscribes to PowerPoint COM events (no polling) to detect slide changes.
- Reads slide notes where `@cuebooth` rules are authored.
- Forwards events to the server over a local named pipe (`\\.\pipe\cuebooth-sidecar`) as one-way, newline-delimited JSON.
- If PowerPoint is ever replaced (e.g., by Keynote, Google Slides, or a CueBooth-native slide app), only this sidecar needs to be replaced.

See [`../docs/design.md`](../docs/design.md) §3.3 ("PowerPoint Monitor") for the full design and §5 (Phase 4 — Slide Engine).

## Status

Skeleton scaffolded (CB-006): a .NET worker host wiring up the two services
below. The COM event handling builds and the named-pipe transport is in place,
but the runtime behavior is only exercised on Windows with PowerPoint — the
event wiring and slide-rule plumbing are fleshed out in Phase 4 of the design.

Layout:

```
sidecar/
├── PptMonitor.csproj        ← .NET worker project (net10.0-windows)
├── Program.cs               ← host entry point; registers the two services
├── SlideMonitor.cs          ← PowerPoint COM event subscriber + payload emitter
├── SidecarPipeServer.cs     ← named-pipe server (newline-delimited JSON to the Go server)
├── appsettings.json         ← logging config
├── appsettings.Development.json
└── Properties/
    └── launchSettings.json
```

## Distribution

The sidecar runs on the same Windows production PC as the server. Release builds will be packaged as a Windows installer via GitHub Actions (see [`../.github/workflows/README.md`](../.github/workflows/README.md)) — likely bundled with the server installer so a single install brings both up together.
