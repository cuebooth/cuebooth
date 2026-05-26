# Operator Runbook

This runbook covers running a hybrid in-person + livestreamed event with CueBooth, end to end. It is intentionally a **template** — every deployment has site-specific equipment, naming, and timing that you will fill in. Use this document as a checklist scaffold; keep your deployment-specific variant alongside it (private to your site) with the actual hardware list, IPs, schedule, and personnel.

This runbook describes operating a deployment **after CueBooth is fully implemented and installed**. During Phase 0–8 (see [design.md](design.md) §5), much of this is still done manually with the underlying tools (OBS, Companion, the mixer's editor app). Sections that depend on un-shipped features are flagged.

---

## Quick reference (fill in for your deployment)

| | |
|---|---|
| Event start time | _e.g. 10:00 AM local_ |
| Stream goes live | _e.g. 10 min before start_ |
| CueBooth server host | _hostname or LAN IP_ |
| CueBooth client device(s) | _iPad, etc._ |
| Slide clicker | _model, location_ |
| Stream platforms | _e.g. YouTube, Facebook (via Restream)_ |

---

## Equipment layout (template)

A typical CueBooth deployment includes:

- **Production PC** — runs the CueBooth server, OBS Studio, Bitfocus Companion, and the slide deck application.
- **Operator surface** — at least one device (tablet, phone, or desktop) running the CueBooth client.
- **Mixer** — OSC-controllable (e.g. Behringer XR-series).
- **One or more PTZ cameras** — VISCA over IP.
- **Slide clicker** — USB HID (Norwii N29 or equivalent).
- **Display surface for slides** — projector or LED wall, driven from the production PC.
- **Speakers** — in-house sound.

Diagram the physical layout for your site and keep it in your deployment-specific runbook. Note any single points of failure (one camera, one mic receiver, etc.) so the operator knows where to direct attention if something fails mid-event.

---

## Part 1 — Pre-event setup

Target completion: at least **30 minutes** before event start.

### 1.1 Power on (sequence matters)

If you have CueBooth's pre-event automation set up (CB-081, Phase 8), this is a one-button operation from the client. Otherwise, follow the manual sequence:

1. Production PC.
2. Wireless mic receivers (in the order their batteries were last charged — least-fresh first, so problems surface early).
3. Mixer.
4. In-house speakers — **after** the mixer is up, to avoid pops.
5. PTZ cameras.
6. Projector / display surface. Confirm it's detected by the PC as an additional display.

Once Phase 8 lands, smart power switches (CB-080) drive most of this from a single sequence.

### 1.2 Software check

The CueBooth server should auto-start on PC login. Verify on the client by connecting to the server — the connection-status indicator should show "connected" within a few seconds.

In OBS, confirm:
- The active scene is your pre-event scene (e.g. countdown + slideshow).
- The virtual camera is started.
- The stream and recording outputs are stopped (you'll start them in Part 2).

In Companion (browser or emulator), confirm:
- All instances show as "OK" (not red).
- The active page is your operator page.

### 1.3 Slides

Open today's slide deck. Verify:
- It's on the correct display (the one captured by OBS).
- It's in presentation mode.
- The slide clicker advances and retreats correctly.

If your deck contains `@cuebooth` automation rules ([slide-rules.md](slide-rules.md)), verify a few of them by advancing to slides you expect to trigger camera/audio changes and confirming the expected behavior.

### 1.4 Pre-roll content (if applicable)

If your event opens with a pre-recorded video that **must not be live-streamed** (e.g. licensed content), the standard pattern is:

- Stream goes live ahead of event start showing a holding scene (countdown + branding).
- Pre-roll video plays **in-house only** during the countdown.
- Video timing is calculated so it ends right at event start.

CueBooth's YouTube pre-roll automation (CB-084, Phase 8) handles the timing math. Until then, calculate start time manually: `video-start = event-start - video-duration`.

### 1.5 Remote operator setup (optional)

If you operate from somewhere other than the production PC (e.g. from the audience), open the CueBooth client on your device:

- Enter the server host (LAN IP, hostname, or Tailscale address).
- Connect.
- Verify video preview is updating (Phase 6 onward).
- Verify mute toggles and a camera preset round-trip from your tap to the actual hardware.

---

## Part 2 — Going live

### 2.1 Start the stream

At your scheduled pre-stream time (e.g. T-10 min):

1. Mute the stream audio (don't broadcast pre-event chatter).
2. Activate your pre-event scene in OBS; reset the countdown if it's a media source.
3. Start streaming in OBS.
4. Start recording in OBS (local backup).
5. Post a welcome message in stream chat.

### 2.2 Start pre-roll (if applicable)

At `T - video-duration`, start the pre-roll video. Confirm the in-house audio path is unmuted so the room hears it.

### 2.3 Transition to live event

This is the hand-off moment with the highest density of changes and the most common place for mistakes. Practice the sequence.

When pre-roll ends (or at event start if no pre-roll):

1. Stop the pre-roll cleanly (before any auto-next-video).
2. Unmute stream audio.
3. Switch OBS to the appropriate live scene.
4. Switch to the opening camera view.
5. Confirm the slide deck is the focused window if you're driving slides with a clicker.

If you have CueBooth slide automation set up, the opening slide can fire most of these via a single `@cuebooth` rule with `apply: immediate`.

---

## Part 3 — Running the event

The exact segment ordering depends on your event format. Build a per-event cue sheet that lists each segment with the expected:

| Segment | Camera view | OBS scene | Audio (active mics) |
|---|---|---|---|
| _e.g. opening music_ | _piano_ | _camera+slides_ | _none_ |
| _e.g. speaker at podium_ | _podium-with-slides_ | _camera+slides_ | _podium, presenter_ |
| _e.g. group performance_ | _stage-wide_ | _camera+slides_ | _stage mics only_ |

For slide-driven events, embed the camera/scene/audio changes as `@cuebooth` rules in slide notes so the operator doesn't need to remember the table. See [slide-rules.md](slide-rules.md).

### General principles

- **One change at a time, when possible.** Stack changes only when they're tightly coupled (e.g. switching to choir = mute non-choir + unmute choir + recall choir camera — three changes, one intent).
- **Watch the meters, not the picture.** A muted mic looks the same as an unmuted one; meters tell you.
- **Bias toward "wider" framings during transitions.** Easier to recover from a too-wide shot than a too-tight one.
- **Don't fight the operator's manual override.** If they pressed a button to mute a mic, automation should respect that until they re-enable it (CB-074).

---

## Part 4 — Post-event teardown

If you have CueBooth's post-event sequence (CB-082, Phase 8), it's one button. Otherwise:

1. Stop the stream in OBS.
2. Stop recording in OBS. Verify the recording file is on disk and not zero bytes.
3. Mute all mics.
4. Power off PTZ cameras (leaving PTZ cameras on 24/7 has been reported to shorten their life).
5. Power off mic receivers.
6. Power off in-house speakers **before** the mixer (avoids the speaker pop you'd otherwise get).
7. Power off the mixer.
8. Update the streaming platform's next-event date if applicable.
9. Shut down the production PC.

---

## Troubleshooting (template)

Document the symptoms you've actually encountered at your site, with the recovery steps. Common patterns to capture:

- **Client can't connect to server.** Check the server process is running on the production PC, the host/port the client is dialing is reachable, and any firewall rules.
- **Slide clicker not advancing slides.** Confirm the clicker dongle is plugged in. Confirm the CueBooth server's HID monitor sees events (CB-051 will add a debug stream).
- **No audio on stream.** Check the stream-output bus on the mixer isn't muted; check the USB audio path between mixer and PC.
- **Camera not responding.** Power-cycle the camera. Verify network reachability from the production PC.
- **OBS dropped frames.** Check upstream bandwidth, CPU usage, and the streaming-platform's status page.
- **Mixer disconnects mid-event.** Lower OSC subscription rate; check WiFi if the mixer is wireless-connected; consider hard-wiring.

---

## Emergency procedures

- **OBS crashes during the stream.** Reopen OBS. The stream is interrupted; restart the stream (and recording). Apologize in chat once back up.
- **Production PC hangs.** Hard restart. The stream drops. Get back live as quickly as possible; rerun the relevant Part 1 steps.
- **Internet outage.** In-person event continues normally without intervention. Stream is dead until connectivity returns; resume the stream when it does.
- **Total audio failure.** Check mixer power, then the USB cable between mixer and PC. If the room still has acoustic coverage from the speakers, the in-person event continues without amplification of remote speakers; the stream is degraded until restored.

---

## See also

- [Design document](design.md) — full system architecture
- [Slide rule authoring guide](slide-rules.md) — `@cuebooth` DSL for slide-driven automation
- [WebSocket protocol](protocol.md) — client/server wire spec
