# Sample Deployment

A worked end-to-end example of a CueBooth deployment. It's drawn from the originating reference deployment — a hybrid in-person + livestreamed weekly worship service — but every piece adapts directly to any similar live event (theater pre-show, conference plenary, school assembly, community broadcast).

Use this document to **see CueBooth working before you build your own deployment**. Pair it with the [design doc](design.md), the [operator runbook](runbook.md), the [slide rule authoring guide](slide-rules.md), and the [protocol spec](protocol.md).

All identifiers below are illustrative: substitute your own IPs, scene names, and preset names.

---

## 1. Event format

Weekly hybrid event, ~75 minutes:

| Time | Segment | What happens |
|---|---|---|
| T-10 min | Pre-roll | Stream goes live with a holding scene (countdown + branding slideshow). In-house plays a short licensed video that **is not** streamed. |
| T | Opening music | Pianist plays an instrumental. Camera on piano. |
| T+5 min | Welcome / announcements | A coordinator speaks from the floor; pastor adds context. |
| T+10 min | Choir piece | Choir sings; pianist accompanies. |
| T+15 min | Readings | Lay reader at the podium; pastor sometimes interjects context. |
| T+25 min | Sermon | Pastor speaks from the podium and altar area. |
| T+50 min | Communion | Pastor speaks; choir distributes; pianist plays underneath. |
| T+65 min | Closing music | Pianist plays; attribution slide; closing image; stream ends. |

The pattern (pre-roll → opening → segments → closing) is universal; only the segment names differ across event types.

---

## 2. Equipment topology

| Role | Example device | Connection |
|---|---|---|
| Production PC | Windows 10/11 desktop | LAN |
| Operator surface (primary) | iPad | WiFi / Tailscale |
| Operator surface (fallback) | Production PC keyboard/mouse | local |
| Mixer | Behringer XR18 (any X-Air protocol mixer works) | USB audio to PC + WiFi OSC |
| Primary PTZ camera | Generic VISCA-over-IP camera | dedicated PoE NIC |
| Backup PTZ camera | Same model, mounted opposite end | second PoE NIC |
| Slide clicker | Norwii N29 or compatible USB HID | USB |
| In-house display | Projector (HDMI as 3rd monitor) | HDMI |
| In-house speakers | Powered FOH speakers | XLR from mixer Main L/R |

Notable choices:
- **Two NICs on the production PC.** One subnet for the cameras' PoE (so camera traffic doesn't share bandwidth with everything else), one for everything else.
- **USB audio path from mixer to PC.** OBS picks up a dedicated stream mix from the mixer's USB bus, kept independent from the in-house mix.
- **Slide deck on a 3rd display.** OBS captures that display as the "slides" source; the operator's two main monitors stay free for tools.

---

## 3. Network

```
                                    ┌─────────────────────┐
                                    │  Tailscale overlay  │
                                    │  (remote operators) │
                                    └──────────┬──────────┘
                                               │
┌──────────────────────────────────────────────┼──────────────────────────┐
│                                              │                          │
│   LAN A — 10.0.0.x  ◄── PC NIC 1 ────────────┴── iPad (operator)        │
│                                                ── Mixer (OSC)           │
│                                                ── (other LAN devices)   │
│                                                                          │
│   LAN B — 10.0.1.x  ◄── PC NIC 2 ────────── Primary camera (PoE switch) │
│                                          ── Backup camera               │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘

         Production PC runs: cuebooth-server, OBS Studio, Companion,
                             PowerPoint, cuebooth-sidecar (PPT COM)
```

The CueBooth client (iPad or other) connects to the server on LAN A or via the Tailscale overlay when off-network.

---

## 4. OBS scenes

Four scenes do nearly all the work:

| Scene | Use |
|---|---|
| `Beginning` | Pre-roll: countdown timer + slideshow images. |
| `Just Camera` | Camera full-frame. Sermon, announcements, prayers. |
| `Camera + Slides` | Camera with slide overlay in a corner. Hymns, readings. |
| `PowerPoint` | Full-screen slides (no camera). Attributions, ending. |

Two more (`Offering`, `Ending`) exist for variants. Keep the active set small.

The mixer's USB bus is the single audio source for OBS; per-source audio is muted in OBS.

---

## 5. Companion preset surface

Companion has connections to: VISCA (cameras), the mixer (XR-series OSC plugin), OBS Studio (WebSocket plugin). The CueBooth server delegates to Companion for everything Companion already does well.

The primary operating page (page 7 in our setup) has:

- **Row 0:** OBS scene buttons (Beginning, Just Camera, Camera+Slides, PowerPoint).
- **Rows 1–2:** Combined macros — each button recalls a camera preset *and* sets the audio mute pattern for that segment. Examples:
  - `Podium` → recall podium camera preset, unmute non-choir DCA, mute choir.
  - `Choir` → recall choir camera preset, mute non-choir DCA, unmute choir.
  - `Piano` → recall piano camera preset, mute everyone except piano.
- **Row 3:** Slide forward/back + quick-access mute toggles.

Other pages: camera presets per camera, mixer DCA controls, stream/recording start-stop, etc.

CueBooth presets in `cuebooth.toml` (§6 below) reference these Companion buttons by their page/row/column coordinates.

---

## 6. `cuebooth.toml` snippet

This is what wires logical preset names (used in slides and the client) to concrete Companion buttons and OSC commands. Edit it once per deployment.

```toml
[server]
listen = "0.0.0.0:7878"

[companion]
base_url = "http://localhost:8000"

[mixer]
host = "10.0.0.50"
port = 10024

[cameras.main]
host = "10.0.1.10"
visca_port = 1259

[cameras.front]
host = "10.0.1.11"
visca_port = 1259

[obs]
host = "127.0.0.1"
port = 4455
# password from environment via OBS_PASSWORD env var

# ─── Camera presets ─────────────────────────────────────────────────────
# Each maps a logical name (used in slide rules + client UI) to a
# Companion button that, when pressed, recalls a VISCA preset. Presets are
# namespaced by camera id ([presets.camera.<id>.<name>]) — all the ones
# below live on the `main` camera; a `front` camera would get its own
# [presets.camera.front.*] entries.

[presets.camera.main.podium]
companion_button = "1/1/1"

[presets.camera.main.podium-with-slides]
companion_button = "1/1/3"

[presets.camera.main.piano]
companion_button = "1/1/4"

[presets.camera.main.altar-wide]
companion_button = "1/1/5"

[presets.camera.main.sanctuary-wide]
companion_button = "1/1/6"

[presets.camera.main.choir]
companion_button = "1/3/2"

# ─── OBS scenes ─────────────────────────────────────────────────────────

[presets.scene.beginning]
companion_button = "7/0/5"

[presets.scene.camera-only]
companion_button = "7/0/1"
# Actual OBS scene: "Just Camera"

[presets.scene.camera-with-slides]
companion_button = "7/0/2"
# Actual OBS scene: "Camera + Slides"

[presets.scene.slides-only]
companion_button = "7/0/3"

# ─── Audio mute targets ─────────────────────────────────────────────────
# DCAs and channels can be muted/unmuted either via Companion (which has
# pre-wired toggles) or via direct OSC — both work for discrete toggles, so
# mix the two as convenient (Companion buttons below, direct OSC below
# that). CueBooth additionally uses direct OSC for fader drag / meter
# streaming, which Companion doesn't cover.

[presets.audio.mute.non-choir]
companion_button = "4/1/1"

[presets.audio.unmute.non-choir]
companion_button = "4/2/1"

[presets.audio.mute.choir]
companion_button = "4/1/2"

[presets.audio.unmute.choir]
companion_button = "4/2/2"

[presets.audio.mute.podium]
osc_command = "/ch/04/mix/on"
osc_value = 0

[presets.audio.unmute.podium]
osc_command = "/ch/04/mix/on"
osc_value = 1

[presets.audio.mute.presenter]
osc_command = "/ch/03/mix/on"
osc_value = 0

[presets.audio.unmute.presenter]
osc_command = "/ch/03/mix/on"
osc_value = 1

[presets.audio.mute.piano]
osc_command = "/ch/10/mix/on"
osc_value = 0

[presets.audio.unmute.piano]
osc_command = "/ch/10/mix/on"
osc_value = 1

# ─── Visible audio channels ─────────────────────────────────────────────
# Which channels appear in the client mixer view, in what order, with
# what labels. Hides the dozens of unused inputs.

[[audio.visible]]
id = "presenter"
label = "Presenter (lapel)"
osc_channel = "/ch/03"

[[audio.visible]]
id = "podium"
label = "Podium"
osc_channel = "/ch/04"

[[audio.visible]]
id = "choir-L"
label = "Choir L"
osc_channel = "/ch/05"

[[audio.visible]]
id = "choir-R"
label = "Choir R"
osc_channel = "/ch/06"

[[audio.visible]]
id = "piano"
label = "Piano"
osc_channel = "/ch/10"

[[audio.visible]]
id = "stream-bus"
label = "Stream"
osc_bus = "/bus/05"
```

---

## 7. A slide deck with `@cuebooth` rules

Six slides, each with the rule block to embed in PowerPoint Notes. The rules reference preset names defined above.

### Slide 1 — Pre-roll holding image

No `@cuebooth` block. The pre-roll happens before the slideshow advances. OBS is on `Beginning`.

### Slide 2 — Opening (pianist plays)

```
@cuebooth
camera.main: piano
scene: camera-with-slides
audio.mute: non-choir, choir
audio.unmute: piano
apply: immediate
```

### Slide 3 — Welcome / announcements

```
@cuebooth
camera.main: sanctuary-wide
scene: camera-only
audio.unmute: presenter, podium
apply: immediate
```

### Slide 4 — Choir piece (slide advanced ahead of reader's announcement)

The reader announces the upcoming piece while still on slide 3. The operator advances to slide 4 *during the announcement* so the title is visible, but the audio/camera changes wait for the confirm press.

```
@cuebooth
camera.main: choir
scene: camera-with-slides
audio.unmute: choir
audio.mute: presenter, podium
apply: on-confirm
```

`apply: on-confirm` defers the **whole** block, so the camera move, scene switch, and audio change all wait and fire together when the operator confirms — the deferred-transition pattern from the [slide rule authoring guide](slide-rules.md). (A similar combined Companion macro exists for manual use from the button grid, though it covers only the camera + audio change — not the scene switch — and uses the non-choir/choir DCA pattern rather than these exact channels; see §5.)

### Slide 5 — First reading at the podium

```
@cuebooth
camera.main: podium-with-slides
scene: camera-with-slides
audio.unmute: podium
audio.mute: choir
apply: on-confirm
```

### Slide 6 — Attributions (end of event)

```
@cuebooth
scene: slides-only
apply: immediate
```

(Camera and audio left where they were; only the OBS scene switches.)

---

## 8. Operator workflow

The operator runs the event from an iPad in the audience, connected to the server over Tailscale. The CueBooth client shows:

- Top: connection status, current OBS scene, slide N of M.
- Center-left: camera joystick + zoom slider for the active camera.
- Center: live OBS preview (low-res screenshot in Phase 6a, live WebRTC in Phase 6b).
- Center-right: fader strips for the channels declared visible in §6.
- Bottom: button grid mirroring the Companion macros (combined presets, OBS scenes, stream/record).
- Side panel: pending actions queue with confirm/cancel buttons.

A typical event run:

1. **T-30 min** — power-on sequence. Phase 8 makes this a single button; until then, follow [runbook §1](runbook.md#part-1--pre-event-setup).
2. **T-10 min** — start stream (Phase 1 button). Pre-event scene + countdown play out.
3. **T - video-duration** — pre-roll video begins. Phase 8 auto-triggers; until then, the operator does this manually.
4. **T** — pre-roll ends. Slide 2 advance fires the opening `@cuebooth` rule. Music plays.
5. **T+5 min** — operator clicks past slide 2; slide 3 fires the welcome rule. Coordinator speaks.
6. **T+10 min** — operator pre-advances to slide 4 during the reader's announcement. The pending-actions panel shows three queued actions. The operator long-presses confirm on the clicker (or taps confirm on the iPad). Camera, scene, and audio switch together.
7. **Through the event** — each slide advance either fires changes immediately or queues them for confirm, per the rule block.
8. **End** — stream stop button + recording stop button. Phase 8 adds full post-event teardown.

---

## 9. What's deployment-specific vs. generic

The bits that vary across deployments:

- IPs, ports, hostnames.
- The mixer channel layout (which physical mic is on which channel).
- The list of Companion presets and which page they live on.
- The set of logical preset names you use in slides (`piano`, `choir`, `podium`...) — chosen to match your event vocabulary.
- The slide deck's segment structure.
- Whether you use pre-roll, communion, multiple cameras, etc.

The bits that stay the same:

- The CueBooth server + sidecar + client codebase.
- The `@cuebooth` DSL.
- The protocol between client and server.
- The runbook's overall shape: pre-event → going live → segments → teardown.

When you stand up your own deployment, copy the example template `cuebooth.example.toml` to `cuebooth.toml` (the name the server loads, used in §6 and elsewhere in the docs) and edit the preset sections. Keep your filled-in version in your own private repo (not in the public CueBooth repo) — it documents your specific install in a way that's useful to your team but not to outsiders.

---

## 10. See also

- [Design document](design.md) — architecture and tech choices
- [Operator runbook](runbook.md) — checklist-shaped operating procedure
- [Slide rule authoring guide](slide-rules.md) — the DSL referenced in §7
- [WebSocket protocol](protocol.md) — wire format for the client/server contract
