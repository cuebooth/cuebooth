# CueBooth Client

The Flutter cross-platform control surface.

## Role

A single app that consolidates everything an operator needs during a live event:

- Companion button grid, rendered natively from Companion's own Satellite surface via the server — auto-discovered with nothing defined client-side (see [`../docs/protocol.md`](../docs/protocol.md) §10).
- Real-time audio meters and fader strips for configured mixer channels.
- Virtual PTZ joystick and zoom slider for cameras (velocity-based, direct via the server).
- Slide status indicator: current slide, pending automation actions, confirm/cancel controls.
- OBS program and preview video (initially as periodic screenshots, later as live WebRTC).
- Stream chat (Restream or platform-direct) and stream/recording status.
- Quick-access channel profiles (e.g., per-speaker EQ presets).

See [`../docs/design.md`](../docs/design.md) §3.5 for details. The client ↔ server wire protocol is specified in [`../docs/protocol.md`](../docs/protocol.md).

## Platform Targets

iPad is the primary target. iPhone, Android, Windows, macOS, Linux, and Web are all supported.

## Status

Scaffolded in Phase 1. The Phase-1 control surface is in place: server connection, the client↔server session/state layer, the Companion Satellite button grid, and the stream/recording status bar. The `lib/` structure:

```
client/
├── pubspec.yaml
├── lib/
│   ├── main.dart
│   ├── services/   WebSocket transport, session, state, surface
│   ├── screens/    Connect screen, main control surface
│   └── widgets/    Surface grid, stream control bar (faders, meters, PTZ joystick in later phases)
├── android/  ios/  macos/  windows/  linux/  web/
```

## Building & running

`flutter pub get` then `flutter run -d <target>` (`macos`, `windows`, `linux`, `chrome`, or a connected device); enter the server's `host:port` on the Connect screen. See [`../docs/development.md`](../docs/development.md) for per-platform toolchain prerequisites and running the full stack.

## Distribution

Mobile builds will be distributed via the App Store and Play Store (or as side-loaded builds during development). Desktop builds — particularly Windows — will be packaged as installers built by GitHub Actions (see [`../.github/workflows/README.md`](../.github/workflows/README.md)).
