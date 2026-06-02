# CueBooth PowerPoint Sidecar

A small C# (.NET) process that watches PowerPoint via COM and forwards slide-change events plus slide metadata to the Go server.

## Role

PowerPoint COM interop is much easier from .NET than from Go. Rather than embedding COM handling in the server, this sidecar isolates it:

- Detects slide changes during an active slideshow (CB-006 polls the show position; CB-040 supersedes this with COM event sinks for lower latency/CPU).
- Reads slide notes where `@cuebooth` rules are authored.
- Forwards events to the server over a local named pipe (`\\.\pipe\cuebooth-sidecar`) as one-way, newline-delimited JSON.
- If PowerPoint is ever replaced (e.g., by Keynote, Google Slides, or a CueBooth-native slide app), only this sidecar needs to be replaced.

See [`../docs/design.md`](../docs/design.md) §3.3 ("PowerPoint Monitor") for the full design and §5 (Phase 4 — Slide Engine).

## Status

Initial implementation (CB-006): a .NET worker host running two services — a
**polling** slide monitor and the named-pipe transport. Polling reads the
active slideshow position on a short timer, which works from an ordinary worker
thread (no STA thread / message pump needed) and is verifiable on Windows with
PowerPoint. CB-040 supersedes polling with COM event sinks (lower latency/CPU);
slide-rule plumbing is fleshed out in Phase 4. COM object release across the
extraction chains is also deferred to CB-040 (tracked inline).

Layout:

```
sidecar/
├── PptMonitor.csproj        ← .NET worker project (net10.0-windows)
├── Program.cs               ← host entry point; registers the two services
├── SlideMonitor.cs          ← polls PowerPoint for slide changes + emits payloads
├── SidecarPipeServer.cs     ← named-pipe server (newline-delimited JSON to the Go server)
├── appsettings.json         ← logging config
├── appsettings.Development.json
└── Properties/
    └── launchSettings.json
```

## Distribution

The sidecar runs on the same Windows production PC as the server. Release builds will be packaged as a Windows installer via GitHub Actions (see [`../.github/workflows/README.md`](../.github/workflows/README.md)) — likely bundled with the server installer so a single install brings both up together.
