# CueBooth Client

The Flutter cross-platform control surface.

## Role

A single app that consolidates everything an operator needs during a live event:

- Button grid for scene switching, camera presets, and mute toggles (mapped to Companion actions via the server).
- Real-time audio meters and fader strips for configured mixer channels.
- Virtual PTZ joystick and zoom slider for cameras (velocity-based, direct via the server).
- Slide status indicator: current slide, pending automation actions, confirm/cancel controls.
- OBS program and preview video (initially as periodic screenshots, later as live WebRTC).
- Stream chat (Restream or platform-direct) and stream/recording status.
- Quick-access channel profiles (e.g., per-speaker EQ presets).

See [`../docs/design.md`](../docs/design.md) §3.4 for details. The client ↔ server wire protocol is specified in [`../docs/protocol.md`](../docs/protocol.md).

## Platform Targets

iPad is the primary target. iPhone, Android, Windows, macOS, Linux, and Web are all supported.

## Status

Not yet scaffolded. The Flutter project will be created in Phase 1 via `flutter create` on a machine with the Flutter SDK installed, then populated with the planned `lib/` structure:

```
client/
├── pubspec.yaml
├── lib/
│   ├── main.dart
│   ├── services/   WebSocket transport, state management
│   ├── screens/    Main control surface, settings
│   └── widgets/    Faders, meters, PTZ joystick, scene buttons
├── android/  ios/  macos/  windows/  linux/  web/
```

## Distribution

Mobile builds will be distributed via the App Store and Play Store (or as side-loaded builds during development). Desktop builds — particularly Windows — will be packaged as installers built by GitHub Actions (see [`../.github/workflows/README.md`](../.github/workflows/README.md)).
