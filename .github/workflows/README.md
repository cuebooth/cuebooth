# CueBooth CI/CD Workflows

GitHub Actions workflows live here. The per-PR/push build-test workflows are implemented (`server.yml`, `client.yml`, `sidecar.yml`); the release/installer builds are still planned, tracked in [CB-087](https://github.com/cuebooth/cuebooth/issues/71). This README captures the full intended set.

## Planned Workflows

### Per-PR / per-push checks (implemented)

| Component | Job | Notes |
|-----------|-----|-------|
| `server/`  | `go build`, `go vet`, `go test ./...` | Run on Linux + Windows runners; the production target is Windows but cross-compilation is trivial. |
| `client/`  | `flutter analyze`, `flutter test` | Run on Linux for speed. |
| `sidecar/` | `dotnet build`, `dotnet test` | Windows runner — uses Office COM types, but build itself can be Linux. |

### Release builds (planned — [CB-087](https://github.com/cuebooth/cuebooth/issues/71))

Triggered by version tags (e.g. `v0.1.0`). All Windows-bound components must produce a real Windows installer.

| Component | Artifact(s) | Tool |
|-----------|-------------|------|
| `server/`  | `cuebooth-server-vX.Y.Z-windows-x64.msi` | TBD — likely [WiX Toolset v4](https://wixtoolset.org/) for a modern MSI. Alternatives: MSIX, Inno Setup. |
| `sidecar/` | `cuebooth-sidecar-vX.Y.Z-windows-x64.msi` (or bundled with server) | Same as above. May ship inside a single combined installer that brings up both components. |
| `client/`  | Windows: `.msix` or `.msi`. macOS: `.dmg`. Linux: AppImage / .deb / .tar.gz. iOS/Android: store-distribution channels. | Flutter's per-platform packaging where it exists; Windows installer built the same way as the server. |

Artifacts are attached to the GitHub Release for the tag.

### Why real installers for Windows

The production PC is typically operated by people who don't want to manage services from a PowerShell prompt. Dropping `.exe` binaries on `C:\` and running `sc create` is not a viable distribution channel. The installer:

- Registers the Go server as a Windows service.
- Installs the C# sidecar in a location where it auto-launches with the server (or with the user session, depending on COM lifecycle).
- Creates any required folders (logs, config) with sensible defaults.
- Provides clean uninstall.

An earlier prototype used a Visual Studio Installer (`.vdproj`), which is deprecated. The new workflows will pick a maintained tool (WiX, MSIX, Inno Setup, etc.) when implemented.

## Status

The per-PR/push build-test workflows are implemented (`server.yml`, `client.yml`, `sidecar.yml`) — landed in #68 (CB-003). The release/installer builds are not yet scaffolded; they're tracked in [CB-087](https://github.com/cuebooth/cuebooth/issues/71) and will land alongside the first version-tagged release.
