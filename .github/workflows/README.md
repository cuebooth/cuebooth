# CueBooth CI/CD Workflows

GitHub Actions workflows live here. The per-PR/push CI workflows are implemented (`server.yml`, `client.yml`, `sidecar.yml` — server and client have a test step, though the server has no tests yet; the sidecar is build-only for now); the release/installer builds are still planned, tracked in [CB-087](https://github.com/cuebooth/cuebooth/issues/71). This README captures the full intended set.

## Planned Workflows

### Per-PR / per-push checks (implemented)

| Component | Job | Notes |
|-----------|-----|-------|
| `server/`  | `go vet`, `go build`, `go test ./...` (native) + `GOOS=windows` cross-build | Runs on a Linux runner. The production target is Windows, reached via a `windows/amd64` cross-build (cross-compilation is trivial) — not a Windows runner. |
| `client/`  | `flutter analyze`, `flutter test` | Run on Linux for speed. |
| `sidecar/` | `dotnet restore`, `dotnet build` (Release) | Runs on `windows-latest` — the Office COM interop types don't restore on Linux. No `dotnet test` step yet (no test project). |

> **Branch-protection note:** these workflows are **path-filtered**, so a PR that doesn't touch a component skips that component's workflow — and a *skipped* check never reports a status. Do **not** mark the path-filtered jobs themselves as *required* status checks: a docs-only or single-component PR would then sit permanently in "Expected" and never become mergeable. If required checks are wanted, add an always-running aggregating gate job (one that runs unconditionally and `needs:` the others) and require that instead.

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

The per-PR/push CI workflows are implemented (`server.yml`, `client.yml`, `sidecar.yml`) — landed in #68 (CB-003). The release/installer builds are not yet scaffolded; they're tracked in [CB-087](https://github.com/cuebooth/cuebooth/issues/71) and will land alongside the first version-tagged release.
