# CueBooth — Project Design Document

**Project Name:** CueBooth
**Version:** 0.1 — Initial Planning
**Date:** May 17, 2026

---

## 1. Executive Summary

CueBooth is a system to automate and simplify the operation of a church worship service that is both in-person and live-streamed. The current setup involves manual coordination of a PTZ camera, digital soundboard (Behringer XR18), OBS Studio, PowerPoint slides, Bitfocus Companion, and multiple streaming platforms via Restream — all requiring significant expertise from the operator.

CueBooth replaces this multi-tool workflow with a unified system consisting of:

- A **server application** running on the sanctuary PC that directly controls all hardware and software.
- A **cross-platform client application** (tablet, phone, desktop, web) providing a single control surface.
- **Slide-driven automation** that derives A/V and scene state from PowerPoint slide metadata.
- **Audio automation** including feedback detection, auto-leveling, and profile switching.

The goal is to reduce operator complexity such that a non-technical person can run a basic service using only a slide clicker, while still allowing experienced operators full manual control.

---

## 2. Current System Inventory

### 2.1 Hardware

| Device | Model | Connection | Protocol | Notes |
|--------|-------|------------|----------|-------|
| PTZ Camera (primary) | AViPAS AV-1281G | PoE, dedicated NIC | VISCA over IP | Controlled via Companion plugin |
| PTZ Camera (backup) | AViPAS AV-1281G | PoE (planned) | VISCA over IP | To be mounted front of sanctuary |
| Digital Soundboard | Behringer XR18 | USB to PC + WiFi | OSC (X-Air protocol), USB audio | Main/aux/USB buses configured |
| Wireless Mic System | MuSysic 4-ch UHF | XLR to XR18 | Analog | 2 of 4 channels operational (1 handheld, 1 lapel) |
| Wireless Lapel Mics | Hotec 900MHz (x2) | XLR to XR18 | Analog | Poor quality, used infrequently |
| Podium Mic | Dynamic (unspecified) | XLR to XR18 | Analog | Adequate quality |
| Spare Handheld | Wireless w/ transmitter | XLR to XR18 | Analog | Infrequent use |
| Choir Mics | Condenser (x2) | XLR to XR18 | Analog | X-pattern stereo, in front of choir |
| Keyboard/Piano | Electric piano | Direct to XR18 | Analog | Separate from mic groups |
| Sanctuary Speakers | 2x main speakers | Main L/R from XR18 | Analog | In-house audio |
| Fellowship Hall Board | 4-channel mixer | Aux send from XR18 | Analog | Sunday school background audio |
| Projector | Unknown model | HDMI from PC (3rd monitor) | — | Manual input selection required |
| Slide Clicker | Norwii N29 | USB dongle | HID (remapped to Shift+F11/F12) | Via Norwii software + AutoHotkey |
| iPad | Apple iPad | WiFi | — | Companion emulator, X-Air, Restream chat, Zoom |

### 2.2 Software (Sanctuary PC — Windows 10)

| Application | Role | Notes |
|-------------|------|-------|
| OBS Studio | Stream composition & output | Multiple scenes, virtual camera, linked to Restream |
| Bitfocus Companion | Macro control surface | HTTP API, on-screen emulator |
| PowerPoint | Slide presentation | 3rd monitor output, controlled via clicker+AHK |
| AutoHotkey v2 | Key remapping | Intercepts Shift+F11/F12/F7/F8 → PowerPoint commands |
| Norwii App | Clicker configuration | Remaps clicker buttons; prone to resetting |
| X-Air Edit | Soundboard control (PC) | Complex UI, used as backup |
| Restream | Multi-platform streaming | Facebook + YouTube, chat auth expires weekly |
| Google Drive | Slide sync | Occasionally breaks on startup |
| Zoom | Remote monitoring | OBS virtual cam + soundboard USB as A/V source |
| Browser | Restream + Companion UI | 3 tabs: restream.io, Companion config, Companion emulator |

### 2.3 XR18 Audio Routing

| Bus/Output | Destination | Purpose |
|------------|-------------|---------|
| Main L/R | Sanctuary speakers | In-house sound |
| Aux/Bus (configured) | USB to PC | Stream audio (independent mix) |
| Aux/Bus (configured) | Fellowship hall mixer | Background audio for Sunday school |

**DCA Groups (virtual faders):**
- Group 1: All non-choir mics
- Group 2: Choir mics (2x condenser)
- Piano: Independent (not in either group)

### 2.4 OBS Scenes

| Scene | OBS Name | Content | Typical Use |
|-------|----------|---------|-------------|
| Beginning | `Beginning` | Countdown timer + slideshow (Opening.png, Announcements.png) | Pre-service (10 min before) |
| Camera + Slides | `Scripture/Announcments` | PTZ camera with slides overlay (upper-left, ~50% scale) | Hymns, readings, responsive readings |
| Camera Only | `Just Camera` | PTZ camera full frame | Sermon, announcements, prayers |
| Slides Only | `PowerPoint` | Slides full frame (Monitor 3 capture) | Attributions, specific readings |

*Note: Offering and Ending scenes exist in OBS but are not actively used.*

### 2.5 Companion Button Groups (from config analysis)

Based on the attached Companion configuration, the primary control surfaces include:
- **Audio mute/unmute toggles** — per-mic and per-group (choir, non-choir)
- **Camera presets** — named positions (Piano, Choir, Podium, Altar Table Wide, Sanctuary Wide, etc.)
- **Combined camera+audio presets** — e.g., "Choir View" sets camera position AND mutes non-choir/unmutes choir
- **OBS scene switching**
- **PowerPoint slide control** (forward/back)
- **Stream start/stop**
- **Recording start/stop**

---

## 3. Architecture Design

### 3.1 High-Level Architecture

The CueBooth server is an **orchestration and automation layer** that delegates to Bitfocus Companion for most hardware control. Companion's plugin ecosystem already handles VISCA, OSC, OBS WebSocket, and dozens of other protocols — reimplementing those would be wasted effort. The server only goes direct to hardware where Companion is inadequate: real-time audio meters, velocity-based PTZ, and OBS video relay.

```
┌──────────────────────────────────────────────────────────────────┐
│                      Sanctuary PC (Windows)                       │
│                                                                  │
│  ┌──────────────┐     HTTP API      ┌───────────────────────┐   │
│  │ CueBooth Server   │◄────────────────►│ Bitfocus Companion     │   │
│  │ (Go)         │                   │                       │   │
│  │              │                   │  ┌─────────────────┐  │   │
│  │  ┌────────┐  │                   │  │ VISCA Plugin    │──┼─► Camera (presets)
│  │  │Slide   │  │                   │  ├─────────────────┤  │   │
│  │  │Engine  │  │                   │  │ OSC Plugin      │──┼─► XR18 (mute toggles)
│  │  ├────────┤  │                   │  ├─────────────────┤  │   │
│  │  │Audio   │  │                   │  │ OBS WS Plugin   │──┼─► OBS (scenes, stream)
│  │  │Engine  │──┼── OSC (direct) ──►│  ├─────────────────┤  │   │
│  │  ├────────┤  │                   │  │ Other Plugins   │  │   │
│  │  │Camera  │  │                   │  └─────────────────┘  │   │
│  │  │Joystick│──┼── VISCA (direct)─►│                       │   │
│  │  ├────────┤  │                   └───────────────────────┘   │
│  │  │HID     │  │                                               │
│  │  │Input   │  │   ┌──────────────┐    ┌────────────────────┐  │
│  │  ├────────┤  │   │ PPT Monitor  │    │ OBS Studio         │  │
│  │  │Video   │──┼── │ (C# sidecar) │    │ (WebSocket API)    │  │
│  │  │Relay   │──┼── OBS WS (direct, screenshots/video only) ─┘  │
│  │  ├────────┤  │   └──────┬───────┘                            │
│  │  │WS API  │  │          │ COM events                         │
│  │  └────────┘  │          ▼                                    │
│  └──────┬───────┘   ┌──────────────┐                            │
│         │           │ PowerPoint   │                            │
│         │           └──────────────┘     ┌──────────────┐       │
│         │ WebSocket + WebRTC             │ XR18         │       │
└─────────┼────────────────────────────────│ (OSC direct) │───────┘
          │                                └──────┬───────┘
          │                                       │
          │                                ┌──────┴───────┐
          │  LAN / Tailscale               │ PTZ Camera   │
          ▼                                │ (VISCA direct│
┌─────────────────────┐                    │  for joystick)│
│ iPad / iPhone       │                    └──────────────┘
│ Android / Desktop   │
│ (Flutter)           │
│                     │
│ ┌─────────────────┐ │
│ │ Unified Control │ │
│ │ Surface         │ │
│ │ - Video Preview │ │
│ │ - Audio Meters  │ │
│ │ - PTZ Joystick  │ │
│ │ - Scene Switch  │ │
│ │ - Slide Monitor │ │
│ │ - Stream Chat   │ │
│ └─────────────────┘ │
└─────────────────────┘
```

### 3.2 Integration Boundaries

The key design principle is: **delegate to Companion unless there's a specific reason not to.**

| Operation | Path | Why |
|-----------|------|-----|
| Camera presets (recall named positions) | Server → Companion HTTP → VISCA plugin → Camera | Companion already stores presets and handles the protocol |
| Camera PTZ joystick (velocity-based) | Server → VISCA direct → Camera | Companion's hold-a-button PTZ is clunky; VISCA velocity commands need continuous control |
| OBS scene switching | Server → Companion HTTP → OBS plugin → OBS | Companion already has the scene names and macros |
| Stream/recording start/stop | Server → Companion HTTP → OBS plugin → OBS | Same as above |
| Audio mute toggles (discrete) | Server → Companion HTTP → OSC plugin → XR18 | Simple on/off, Companion handles it |
| Audio faders (continuous) | Server → OSC direct → XR18 | UDP is much more responsive than HTTP round-trips for fader drag gestures |
| Audio meters (real-time) | Server ← OSC direct ← XR18 | Companion doesn't expose meter data; XR18 streams it via OSC subscription |
| Audio gain/EQ/profiles | Server → OSC direct → XR18 | Already connected for meters; direct OSC gives full parameter access |
| Audio automation (feedback, leveling) | Server → OSC direct → XR18 | Needs raw meter data + fast response |
| OBS video preview relay | Server → OBS WebSocket direct → OBS | Companion doesn't expose screenshot/video capture |
| Combined macros (camera+audio+scene) | Server → Companion HTTP (single trigger) | Companion already has combined presets wired up |
| Slide-driven actions | Slide Engine → routes to Companion HTTP or direct as appropriate | Automation layer orchestrates both paths |

### 3.3 Component Breakdown

#### CueBooth Server (Go)

The orchestration and automation daemon running on the sanctuary PC. It does NOT reimplement protocol-level control for most hardware — it delegates to Companion for that. Its primary roles are:

- **Automation:** Execute slide-driven rules, audio automation, pre/post-service sequences.
- **Orchestration:** Coordinate actions across Companion, direct OSC, and direct VISCA into unified workflows.
- **Client API:** Serve a WebSocket API that the Flutter client connects to, providing a single control surface.
- **Direct hardware (where needed):** OSC for audio meters/faders/automation, VISCA for joystick PTZ, OBS WebSocket for video relay.

**Why Go:**
- Compiled, single binary deployment — no runtime to install or manage.
- Excellent concurrency model (goroutines) — ideal for managing simultaneous connections (Companion HTTP, OSC UDP, VISCA TCP, OBS WebSocket, client WebSocket, HID).
- Strong networking stdlib — HTTP client (for Companion API), WebSocket, UDP (for OSC).
- Mature OSC libraries (`hypebeast/go-osc` or `scgolern/osc`).
- Can run as a Windows service via `golang.org/x/sys/windows/svc` or wrapped with NSSM.
- Cross-compiles trivially if you ever move the server to Linux.
- Pragmatic and fast to develop in given your background.

**Go vs alternatives considered:**
- **Rust:** More powerful type system but steeper learning curve. The async model adds complexity. The server isn't performance-critical — it's I/O bound to network devices.
- **C#/.NET:** Natural for Windows services and COM interop. Heavier deployment. The COM advantage is handled by the sidecar.
- **Node/Deno:** Interpreted, larger footprint. Not your preference.
- **C++:** You know it well, but development velocity for network services is much lower.

#### Companion Integration Layer

The server talks to Companion via its HTTP API (`http://localhost:8000`). Key operations:
- **Button press:** Trigger any configured Companion button (which may execute multi-action macros — camera preset + mute changes + scene switch in one call).
- **Button state:** Read the current state of toggle buttons (mute on/off, scene active).
- **Variable read/write:** Access Companion variables for dynamic state.

This means the existing Companion configuration continues to work and evolve independently. New plugins can be added to Companion without any changes to the CueBooth server. The server simply needs to know which Companion button IDs map to which logical actions — this is stored in the CueBooth config file.

#### PowerPoint Monitor (C# Sidecar)

A small, focused process (~200 lines) that handles PowerPoint COM automation:
- Connects to PowerPoint via COM events (not polling) to detect slide changes.
- Reads slide metadata/comments (where CueBooth rules are defined).
- Forwards events to the Go server over a local named pipe or localhost WebSocket.
- If PowerPoint is ever replaced, only this sidecar changes.

C# is used because COM interop in Go is painful, and .NET is already on every Windows machine.

#### HID Input Handler

Captures raw HID events from the Norwii N29 clicker. This replaces:
- The Norwii remapping software (which resets unpredictably).
- The AutoHotkey script.

The Go server reads the USB HID device directly, interprets button presses (short, long, double), and routes them to configurable actions. This makes the clicker fully programmable and reliable.

#### Audio Engine (Direct OSC)

Communicates with the XR18 via OSC over UDP. This bypasses Companion because:
- Companion doesn't expose real-time meter data.
- Continuous fader control needs UDP's low latency, not HTTP round-trips.
- Audio automation requires raw meter access and fast response times.

Capabilities:
- Subscribe to and stream real-time audio meters to the client.
- Read/write fader levels, gain, EQ, bus sends with low latency.
- Implement automation: feedback detection, auto-leveling (target LUFS), speech activity detection for auto-mute.
- Store and recall channel profiles (e.g., "Pastor EQ" vs "Guest Pastor EQ").

Note: simple mute toggles can go through either Companion or direct OSC. Since the OSC connection exists anyway, mute commands from the client fader UI go direct. Companion's mute buttons continue to work independently — the XR18 handles concurrent OSC clients.

#### Camera Joystick (Direct VISCA)

VISCA over IP to the AV-1281G, used ONLY for velocity-based PTZ control. Companion continues to handle camera preset recall.

The VISCA `Pan-TiltDrive` command accepts pan speed (0x01–0x18) and tilt speed (0x01–0x11) parameters. The Flutter client presents a virtual joystick where displacement from center maps to velocity. Releasing the joystick sends a stop command. Zoom works the same way with a vertical slider mapping to zoom speed.

This gives smooth, proportional camera control that Companion's "hold a button for a fixed time" approach can't match.

#### Video Relay (Direct OBS WebSocket)

Delivers OBS program and preview feeds to connected clients. Companion doesn't expose this capability.

- **Phase 1:** Periodic JPEG snapshots via OBS WebSocket `GetSourceScreenshot`. Low bandwidth, ~2-5 fps. Good enough for monitoring.
- **Phase 2:** SRT/RTMP output from OBS → Go server → WebRTC to clients. Near real-time preview.

#### Slide Engine

The automation brain. When a slide change is detected:
1. Receive slide change event + metadata from the C# sidecar.
2. Parse the rule definitions from the slide's comments.
3. Determine which actions to execute immediately vs. queue for operator confirmation.
4. Route immediate actions to the appropriate path (Companion HTTP for presets/scenes, direct OSC for audio, etc.).
5. Queue deferred actions and signal the client (and/or clicker).

### 3.5 Slide Rule Format (Draft)

Rules are embedded in PowerPoint slide comments (or notes). Format is a simple DSL. Rules reference **logical preset names** defined in the server config, which map to Companion button IDs and/or direct OSC commands.

```
@cuebooth
camera: choir
scene: camera+slides
audio.mute: non-choir
audio.unmute: choir
apply: immediate
```

```
@cuebooth
camera: podium-slides
scene: camera+slides
audio.mute: choir
audio.unmute: podium, pastor
apply: on-confirm
```

The server config maps these names to actual actions:

```toml
# cuebooth.toml (excerpt)
[presets.camera.choir]
companion_button = "1/0/2"     # page/row/column in Companion

[presets.scene.camera+slides]
companion_button = "1/3/1"
# Actual OBS scene: "Scripture/Announcments"

[presets.audio.mute.non-choir]
companion_button = "1/1/0"     # OR direct OSC:
# osc_command = "/ch/01/mix/on"
# osc_value = 0
```

- `apply: immediate` — actions execute as soon as the slide changes.
- `apply: on-confirm` — actions queue until the operator presses the confirm button on the clicker.
- Slide authors use friendly preset names; the server config handles the routing details.
- A service-level config file defines defaults, preset mappings, and override behavior.

### 3.4 Client Application (Flutter)

A single app that consolidates:
- Companion-style button grid (camera presets, mute toggles, scene switches).
- OBS program/preview video.
- Audio meters and fader controls for selected channels.
- Stream chat (embedded Restream chat or direct API).
- Slide status indicator (current slide, upcoming automation, confirm button).
- Stream status (live/offline, viewer count, recording status).
- Quick-access channel profiles (EQ presets per mic).

**Platform targets:** iPad (primary), iPhone, Android, Windows, macOS, Linux, Web (fallback).

**Framework:** Flutter (Dart). Compiles to native binaries on all targets — no bridge, no JS runtime. Desktop support (Windows, macOS, Linux) is first-party and stable. Web output uses WASM/Canvas (heavier than typical web apps, but fine for a control surface — not a public site). The widget composition model and reactive state management map well to a real-time control surface with meters, faders, and live video.

### 3.5 Communication Protocol

Client ↔ Server communication is over WebSocket with JSON messages. The server is authoritative — clients send commands, server broadcasts state updates.

```json
// Client → Server: command
{ "type": "cmd", "target": "camera", "action": "preset", "value": "choir" }

// Server → Client: state update
{
  "type": "state",
  "audio": {
    "channels": {
      "pastor-lapel": { "mute": false, "fader": -6.2, "gain": 32, "meter": -18.4 },
      "podium": { "mute": true, "fader": -8.0, "gain": 28, "meter": -60.0 }
    }
  },
  "camera": { "preset": "choir", "pan": 128, "tilt": 45, "zoom": 200 },
  "obs": { "scene": "Scripture/Announcments", "streaming": true, "recording": true },
  "slides": { "current": 5, "total": 24, "pendingActions": 2 },
  "stream": { "platform": "restream", "viewers": 12, "uptime": "00:42:15" }
}
```

Audio meters are sent at a higher frequency (~10 Hz) on a separate WebSocket channel or as a distinct message type to avoid flooding the main state channel.

### 3.6 Remote Access

The server binds to `0.0.0.0` (or a configurable interface). For remote access:
- On the local network: direct connection.
- Remotely: via Tailscale (already in use). The Flutter app connects to the server's Tailscale IP. No additional VPN or tunneling infrastructure needed.

---

## 4. Technology Decisions Summary

| Component | Technology | Rationale |
|-----------|-----------|-----------|
| Server | Go | Compiled, concurrent, excellent networking, single binary |
| PPT Monitor | C# (.NET) | First-class COM interop, tiny focused sidecar |
| Client | Flutter (Dart) | Compiled native, true cross-platform (mobile + desktop + web), strong UI toolkit for real-time controls |
| Client ↔ Server | WebSocket (JSON) | Bidirectional, real-time, works over Tailscale |
| Hardware Control Hub | Bitfocus Companion (HTTP API) | Existing plugin ecosystem for VISCA, OSC, OBS; no need to reimplement |
| Audio Meters/Faders/Automation | OSC direct to XR18 | Companion doesn't expose meters; UDP needed for fader latency |
| Camera Joystick | VISCA direct to camera | Velocity-based PTZ needs continuous control Companion can't provide |
| Video Relay (Phase 1) | OBS WebSocket screenshots | Companion doesn't expose this; simple first implementation |
| Video Relay (Phase 2) | SRT/RTMP → WebRTC | Low-latency live preview |
| HID Input | Raw USB HID (Go) | Bypass Norwii app + AHK entirely |
| Slide Rules | Custom DSL in slide comments | Human-readable, version-controlled with slides |
| Remote Access | Tailscale | Already deployed, zero additional config |
| Project Management | GitHub Projects | Already in use for other projects |
| Source Control | GitHub (monorepo) | Server + client + docs in one repo |

---

## 5. Implementation Phases

### Phase 0: Foundation & Documentation (This Session)
- [x] Document current system and manual procedures.
- [x] Finalize architecture design (this document).
- [ ] Create GitHub repo and project board.
- [ ] Set up initial project structure (Go server, Flutter client, C# sidecar).

### Phase 1: Server Core + Companion Integration
**Goal:** Prove out the server architecture by wrapping Companion's HTTP API. Get the Flutter client talking to the server and controlling things through Companion.

- Set up Go project, build system, Windows service scaffolding.
- Implement Companion HTTP API client (button press, button state, variables).
- Server configuration file mapping logical actions to Companion button IDs.
- WebSocket API server: client connections, command routing, state broadcast.
- Minimal Flutter client: connect to server, trigger Companion buttons for scene switching, stream start/stop, camera presets.

**Milestone:** Can control existing Companion buttons from the Flutter app, routed through the server. Everything Companion does today is accessible from the new client.

### Phase 2: Audio Control (Direct OSC)
**Goal:** Direct XR18 control for meters, faders, and profiles. This is the biggest improvement over the current iPad workflow.

- Implement OSC client for XR18 in Go.
- Subscribe to and stream real-time audio meters to clients.
- Read/write fader levels, gain with low-latency OSC.
- DCA group control.
- Channel profile storage/recall (EQ presets for pastor vs guest).
- Configurable channel visibility (hide unused channels in client).
- Client UI: fader strips for selected channels, meters, mute buttons, gain knobs, profile selector.

**Milestone:** Audio mixing from the Flutter app with live meters. X-Air app no longer needed during service.

### Phase 3: Camera Joystick (Direct VISCA)
**Goal:** Smooth, velocity-based PTZ control. Camera presets continue to go through Companion.

- Implement VISCA over IP client in Go (velocity PTZ commands only — `Pan-TiltDrive`, `Zoom` with speed parameters).
- Map joystick displacement → velocity → VISCA speed parameters.
- Stop command on joystick release.
- Client UI: virtual joystick (touch-based, drag from center), zoom slider.
- Multi-camera support: address second camera when installed.

**Milestone:** Smooth proportional camera control from the client. Camera presets still work via Companion buttons.

### Phase 4: Slide Engine
**Goal:** Slide-driven automation of camera, audio, and OBS scenes.

- Build C# sidecar for PowerPoint COM event monitoring.
- Define and implement the slide rule DSL.
- Server-side rule parser and action executor.
- Immediate vs. deferred action system.
- Actions route to Companion HTTP (for presets/scenes) or direct OSC (for audio) as appropriate.
- Client UI: current slide indicator, pending actions display, confirm/cancel controls.
- Service configuration file format (mapping preset names to Companion buttons and OSC paths).

**Milestone:** Advancing slides automatically triggers correct camera, audio, and OBS state. Operator can override. "Clicker-only" basic mode works for simple services.

### Phase 5: HID Input & Clicker Independence
**Goal:** Eliminate Norwii software and AutoHotkey. Full clicker programmability.

- Raw USB HID capture in Go for the Norwii N29 dongle.
- Map all physical buttons (short press, long press, double press) to configurable actions.
- Default mapping: forward = next slide, back = prev slide, long-press forward = confirm pending actions, long-press back = cancel pending actions.
- Optional: other buttons for quick mute toggle, camera preset cycle.

**Milestone:** Norwii app and AutoHotkey uninstalled. Clicker works reliably with no external dependencies.

### Phase 6: Video Preview Relay
**Goal:** OBS program and preview video visible in the client, eliminating Zoom.

- Phase 6a: OBS WebSocket screenshot-based preview (~2-5 fps). Minimal effort, sufficient for monitoring.
- Phase 6b: SRT/RTMP output from OBS → Go server → WebRTC to clients. Real-time preview with low latency.

**Milestone:** Zoom is no longer needed. Operator sees exactly what viewers see, directly in the app.

### Phase 7: Audio Automation
**Goal:** Intelligent audio management reducing operator burden.

- Feedback detection: monitor XR18 meter data for sustained narrow-band peaks, auto-mute or notch-filter the offending channel.
- Auto-leveling for stream: target LUFS measurement, adjust USB bus output to maintain consistent stream volume.
- Auto-mute/unmute: detect speech activity on mics, auto-mute silent mics to reduce noise floor.
- In-house auto-leveling: requires ambient level measurement (external mic or dedicated measurement mic). Stretch goal — may need additional hardware (SPL sensor or reference mic).

**Milestone:** Stream audio levels are consistent without manual adjustment. Feedback events are caught and suppressed automatically.

### Phase 8: Setup Automation & Polish
**Goal:** Automate pre-service and post-service routines.

- Smart power control: network-controllable power strips/switches for speakers, mic receivers, camera (investigate options — Kasa/Tapo smart plugs, networked PDUs).
- Pre-service sequence: single button to power on all equipment, launch OBS, open slides, set Beginning scene, start countdown. Executed via a mix of Companion buttons and direct commands.
- Post-service sequence: stop stream/recording, mute all, power off camera, update Restream date, shut down.
- Opening slide/announcement image auto-extraction from PowerPoint (replace the manual Paint workflow).
- YouTube pre-roll video: auto-detect video link from slide, calculate start time, manage playback and transition.
- Google Drive reliability: auto-detect broken state, kill/restart, or switch to OneDrive/direct sync alternative.

**Milestone:** A single button starts the entire pre-service sequence. Post-service teardown is equally automated.

---

## 6. Story Breakdown (GitHub Issues)

Below is a suggested set of GitHub issues organized by phase. Each is scoped to be independently completable. Labels: `server`, `client`, `sidecar`, `docs`, `infra`.

### Phase 0 — Foundation
- **CB-001** `docs` — Write operational runbook for manual worship service operation
- **CB-002** `docs` — Finalize project design document
- **CB-003** `infra` — Create GitHub repo, project board, and CI scaffolding
- **CB-004** `server` — Go project skeleton: module init, directory structure, config loading, Windows service wrapper
- **CB-005** `client` — Flutter project skeleton: multi-platform setup, WebSocket connection scaffold, basic navigation
- **CB-006** `sidecar` — C# project skeleton: .NET console app, PowerPoint COM interop proof-of-concept

### Phase 1 — Server Core + Companion Integration
- **CB-010** `server` — Companion HTTP API client: button press, button state read, variable read/write
- **CB-011** `server` — Configuration file format (TOML): logical action names → Companion button IDs mapping
- **CB-012** `server` — WebSocket API server: client connections, command routing, state broadcast
- **CB-013** `server` — State aggregation: poll Companion button states, build unified state object for clients
- **CB-014** `client` — Server connection screen: IP/port entry (with Tailscale IP support), connection status
- **CB-015** `client` — Main control surface: button grid mapped to Companion actions (scenes, presets, mutes)
- **CB-016** `client` — Stream/recording status indicators and start/stop controls
- **CB-017** `client` — Restream chat integration (embedded webview or direct API)

### Phase 2 — Audio Control (Direct OSC)
- **CB-020** `server` — XR18 OSC client: connect, subscribe to meters, read/write channel parameters
- **CB-021** `server` — Real-time meter data parsing and streaming to clients (10 Hz)
- **CB-022** `server` — DCA group control via OSC
- **CB-023** `server` — Channel profile system: store/recall EQ, gain, and fader presets per channel
- **CB-024** `server` — Configurable channel visibility (which channels/buses appear in client)
- **CB-025** `client` — Audio mixer UI: vertical fader strips with touch-drag control
- **CB-026** `client` — Real-time audio meter bars (peak + RMS)
- **CB-027** `client` — Mute buttons and gain knobs per channel
- **CB-028** `client` — Channel profile selector (quick-switch between EQ presets)

### Phase 3 — Camera Joystick (Direct VISCA)
- **CB-030** `server` — VISCA over IP client: velocity PTZ commands, stop commands
- **CB-031** `server` — Joystick input mapping: displacement magnitude → VISCA speed parameter
- **CB-032** `server` — Zoom speed control: slider value → VISCA zoom speed
- **CB-033** `client` — Virtual PTZ joystick widget: touch drag, spring-back to center, continuous position streaming
- **CB-034** `client` — Zoom slider widget: vertical, spring-back to center
- **CB-035** `server` — Multi-camera addressing for second camera

### Phase 4 — Slide Engine
- **CB-040** `sidecar` — PowerPoint COM event-based slide change detection
- **CB-041** `sidecar` — Slide metadata/comment extraction and IPC to Go server
- **CB-042** `server` — Slide rule DSL parser
- **CB-043** `server` — Rule action executor: route to Companion HTTP or direct OSC, immediate vs. deferred
- **CB-044** `server` — Service configuration file: preset names → Companion button IDs + OSC paths
- **CB-045** `client` — Slide status panel: current slide number, title, pending actions
- **CB-046** `client` — Confirm/cancel buttons for deferred actions
- **CB-047** `docs` — Slide rule authoring guide for worship coordinators

### Phase 5 — HID Input
- **CB-050** `server` — Raw USB HID device enumeration and Norwii N29 identification
- **CB-051** `server` — HID button event capture: short press, long press, double press detection
- **CB-052** `server` — Configurable HID button-to-action mapping
- **CB-053** `docs` — Clicker button mapping reference card (printable)

### Phase 6 — Video Preview
- **CB-060** `server` — OBS WebSocket client: connect, authenticate, request screenshots
- **CB-061** `server` — Screenshot relay: periodic JPEG capture, serve to clients via WebSocket
- **CB-062** `client` — Program/preview video display (screenshot mode)
- **CB-063** `server` — RTMP/SRT ingest from OBS, WebRTC relay to clients
- **CB-064** `client` — Live video preview via WebRTC

### Phase 7 — Audio Automation
- **CB-070** `server` — Feedback detection algorithm: meter analysis, frequency identification
- **CB-071** `server` — Auto-mute on feedback: suppress offending channel, notify client
- **CB-072** `server` — Stream auto-leveling: target LUFS, USB bus output adjustment
- **CB-073** `server` — Speech activity detection: auto-mute idle mics
- **CB-074** `client` — Audio automation status panel: active rules, overrides, alerts

### Phase 8 — Setup Automation
- **CB-080** `server` — Smart power control integration (Kasa/Tapo API or similar)
- **CB-081** `server` — Pre-service automation sequence: power on, launch apps, set scene, start countdown
- **CB-082** `server` — Post-service automation sequence: stop stream, power off, update Restream
- **CB-083** `server` — Auto-extract opening/announcement images from PowerPoint slides
- **CB-084** `server` — YouTube pre-roll: parse video URL from slide, calculate timing, manage playback
- **CB-085** `client` — One-button pre-service and post-service triggers
- **CB-086** `server` — Google Drive health check and auto-restart

---

## 7. Open Questions & Risks

1. **Projector automation:** You noted this likely can't be automated. Confirm whether the projector supports RS-232, IP control, or CEC over HDMI. Many projectors do — this might be solvable.

2. **XR18 OSC concurrency:** The XR18 supports a limited number of simultaneous OSC clients (typically 4). The Go server's direct OSC connection counts as one. X-Air Edit on the iPad (if still used during transition) counts as another. Companion's OSC plugin counts as a third. Should stay within limits, but monitor this. Once the Flutter app replaces X-Air Edit, that frees a slot.

3. **HID device access on Windows:** Raw HID access may require running the server as administrator or using a filter driver (e.g., HidGuard) to prevent Windows from also processing the HID events. Needs investigation in Phase 5.

4. **WebRTC complexity (Phase 6b):** WebRTC in Go is possible (via Pion) but non-trivial. Phase 6a (screenshots) may be sufficient for a long time. Don't let Phase 6b block other work.

5. **Feedback detection accuracy (Phase 7):** Purely meter-based feedback detection has limits. May need FFT analysis of the audio stream (the USB output) rather than just meter levels. Evaluate whether the XR18's meter data is granular enough.

6. **Second camera:** When the backup AV-1281G is installed, the server needs to manage two VISCA connections for joystick control, and Companion needs a second set of presets. The slide rule DSL needs a `camera-id` field.

7. **Companion API stability:** The CueBooth server depends on Companion's HTTP API. Companion v3 changed some API patterns from v2. Pin the Companion version and test API compatibility before upgrading Companion. Companion's API is documented but not versioned with stability guarantees.

8. **Companion button ID mapping maintenance:** The CueBooth config maps logical action names to Companion button IDs. If someone reorganizes the Companion layout, the mapping breaks. Consider using Companion variables or named triggers (available in Companion 3.x) instead of raw button coordinates where possible.

---

## 8. Repository Structure (Proposed)

```
cuebooth/
├── README.md
├── docs/
│   ├── design.md              ← This document
│   ├── runbook.md             ← Manual operation runbook
│   ├── slide-rules.md         ← Rule authoring guide
│   └── clicker-reference.md   ← Printable button mapping
├── server/                    ← Go server
│   ├── go.mod
│   ├── cmd/
│   │   └── cuebooth-server/
│   │       └── main.go
│   ├── internal/
│   │   ├── companion/         ← Companion HTTP API client
│   │   ├── audio/             ← XR18 direct OSC client + meters + automation
│   │   ├── camera/            ← VISCA velocity PTZ (joystick only)
│   │   ├── obs/               ← OBS WebSocket client (video relay only)
│   │   ├── slides/            ← Rule parser + executor
│   │   ├── hid/               ← USB HID input
│   │   └── api/               ← WebSocket API server for clients
│   └── configs/
│       └── cuebooth.toml           ← Server config (action→Companion button mappings, etc.)
├── client/                    ← Flutter app
│   ├── pubspec.yaml
│   ├── lib/
│   │   ├── main.dart
│   │   ├── services/          ← WebSocket, state management
│   │   ├── screens/           ← Main control surface, settings
│   │   └── widgets/           ← Faders, meters, PTZ joystick, buttons
│   ├── android/
│   ├── ios/
│   ├── macos/
│   ├── windows/
│   └── linux/
├── sidecar/                   ← C# PowerPoint monitor
│   ├── PptMonitor.csproj
│   └── Program.cs
└── .github/
    └── workflows/             ← CI: build server, client, sidecar
```