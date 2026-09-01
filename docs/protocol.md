# CueBooth Client ↔ Server Protocol

**Version:** v1 (draft)
**Transport:** WebSocket, JSON text frames
**Endpoint:** `/ws` on the server's HTTP listener
**Meter endpoint:** `/ws/meters` (see [Meter channel](#6-meter-channel))
**Chat endpoints:** `/chat/*`, plain HTTP on the same listener (see [Chat endpoints](#11-chat-endpoints))

This document is the normative spec for the wire protocol between a CueBooth client (typically the Flutter app) and the cuebooth-server (the Go orchestrator). Server and client implementations should be developed against this spec rather than against each other.

> **Scope:** this covers the client↔server WebSocket protocol, plus the small HTTP surface in §11 that chat needs because its authorization is a browser round-trip. The PowerPoint sidecar reaches the server over a separate local named pipe (newline-delimited JSON); that channel's payload contract is formalized in CB-041, not here.

The design rationale is in [design.md](design.md) §3.6 *Communication Protocol*. This document fills in the details that §3.6 only sketches.

---

## 1. Connection lifecycle

1. Client opens a WebSocket to `ws://<host>:<port>/ws`.
2. Server immediately sends a `hello` frame. Clients MUST NOT send any frame on `/ws` (`cmd`, `subscribe`/`unsubscribe`, `get_state`, or `ping`) until they have received `hello`; servers MUST send it within 500 ms of accepting the socket.
3. Server then sends an initial `state` snapshot for the client's default subscription (all non-meter topics) — see [§4](#4-server--client-messages). No `subscribe` or `get_state` is required to receive it.
4. Client opens a *second* WebSocket to `/ws/meters` if it wants high-rate meter data. This is independent of `/ws` — it has its own lifecycle, no `hello`, and only carries meter frames.
5. Either side may close at any time. Clients SHOULD reconnect with exponential backoff (1s → 30s cap).

### Authentication

v1 has no in-protocol auth. Deployments rely on network-level isolation (LAN + Tailscale per [design.md](design.md) §3.7 *Remote Access*). A future revision will add a token handshake; that's out of scope for v1.

### Versioning

The `hello` frame carries a `proto` field naming the protocol version. The document's "v1" label denotes this protocol's **major** version; the current on-wire `proto` string is `1.1` — so "v1" and `proto: "1.1"` refer to the same protocol. The `proto` string is `MAJOR.MINOR`, where both components are non-negative integers with no leading zeros; the major version is the substring before the first `.`, compared as an integer. Clients MUST refuse to operate against a server whose `proto` differs in major version. Minor-version bumps are additive and backwards-compatible (new optional fields, new `type` values clients can safely ignore).

```json
{
  "type": "hello",
  "proto": "1.1",
  "server_version": "0.1.0",
  "server_id": "production-pc"
}
```

When v2 lands, version negotiation is expected to use the WebSocket subprotocol mechanism (`Sec-WebSocket-Protocol`) or a separate versioned endpoint; the concrete scheme is out of scope for v1. (A v1 server cannot infer the client's version from the live connection, since it MUST send `hello` before receiving any client frame — so negotiation has to happen at or before the handshake.) Clients should not assume v1 servers will ever be retrofitted with v2.

---

## 2. Envelope

Every frame is a single JSON object with a `type` field that determines the rest of the shape.

```json
{ "type": "<message-type>", ... }
```

Unknown `type` values MUST be ignored (forwards compatibility). Malformed JSON MUST result in a connection close (code 1007, "Invalid frame payload data"). Servers SHOULD log such events for debugging.

Field naming convention: `snake_case`.

---

## 3. Client → Server messages

### `cmd` — execute an action

A client request to mutate state. The server executes the action (via Companion, OSC, VISCA, etc. as appropriate). An accepted `cmd` is always `ack`'d, and — if it changed modeled state — the change is broadcast in the next `state` or `state-delta` frame. Commands that produce no modeled state change (e.g. `power`/`automation` targets, or a no-op `confirm_pending`/`cancel_pending`) are `ack`'d with no following state frame, so clients MUST NOT block waiting for a delta to consider a command done.

```json
{
  "type": "cmd",
  "id": "c123",
  "target": "camera",
  "action": "preset",
  "value": "choir"
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `id` | string | yes | Client-chosen correlation ID. Echoed back in any `ack`/`nak` for this command. |
| `target` | string | yes | One of: `camera`, `audio`, `scene`, `slides`, `streaming`, `recording`, `power`, `automation`. Other targets MAY be added in minor versions. |
| `action` | string | yes | Per-target verb; see [§5 Actions catalog](#5-actions-catalog). |
| `value` | any | depends | Per-action payload. May be string, number, bool, or object. |
| `camera_id` | string | depends | Required for `target: camera` in multi-camera deployments. Optional and ignored in single-camera setups, where the lone camera is keyed `main` in `state.camera` (see §4). |

**Target → state mapping.** A `target` is an operator-meaningful verb object; it does not always share its name with the state key (or subscription topic) it affects. The protocol deliberately abstracts the underlying tools rather than exposing them, so some targets map onto the `obs` domain:

| `target` | Mutates state under | Subscribe topic to watch |
|---|---|---|
| `camera` | `camera` | `camera` |
| `audio` | `audio` | `audio` |
| `scene` | `obs.scene` | `obs` |
| `streaming` (start/stop) | `obs.streaming` | `obs` |
| `recording` | `obs.recording` | `obs` |
| `slides` | `slides` | `slides` |
| `power` / `automation` | — (no state key; deferred/advisory) | — |

Note the `streaming` **target** (start/stop, reflected in `obs.streaming` and watched via the `obs` topic) is named distinctly from the `stream` **state key** and its matching `stream` topic, which carry streaming-platform metadata (`platform`, `viewers`). A client watching live on/off status subscribes to `obs`, not `stream`. (The target was renamed from `stream` to `streaming` precisely to avoid this collision — start/stop verbs map to `obs.streaming`, while the `stream` key/topic carries platform info.)

### `subscribe` / `unsubscribe`

Opt in or out of state-update streams. v1 supports subscribing to topics; the default subscription is all topics (meters are separate — they have their own `/ws/meters` endpoint, see §6).

The valid v1 topics are: `audio`, `camera`, `obs`, `slides`, `stream`. (Meters are not a topic — they have their own `/ws/meters` endpoint.) Subscribing to or unsubscribing from any other topic string is a protocol violation and yields an `error` with code `unknown_topic`. New topics MAY be added in minor versions.

```json
{ "type": "subscribe",   "topics": ["audio", "camera", "obs", "slides"] }
{ "type": "unsubscribe", "topics": ["slides"] }
```

If a client never sends `subscribe`, it is implicitly subscribed to all non-meter topics.

### `get_state`

Request a fresh full `state` snapshot for the **current** subscription, without changing it. This is the dedicated re-sync mechanism — use it to recover after a detected `rev` gap (see [`state-delta`](#state-delta--partial-update)) instead of toggling the subscription.

```json
{ "type": "get_state" }
```

The server responds with a `state` frame scoped to the topics the client is currently subscribed to.

### `ping`

Application-level keepalive. Server replies with `pong` carrying the same `id`.

```json
{ "type": "ping", "id": "k42" }
```

(WebSocket-level ping/pong frames are also fine; this is an application alternative.)

---

## 4. Server → Client messages

### `hello`

See [Connection lifecycle](#1-connection-lifecycle).

### `state` — full state snapshot

Sent once after `hello`, again whenever a client changes its subscription (`subscribe`/`unsubscribe`) or requests `get_state`, and after a server-side reset. A `state` snapshot contains only the topics the client is currently subscribed to; the example below shows the default subscription (all non-meter topics).

```json
{
  "type": "state",
  "rev": 142,
  "audio": {
    "channels": {
      "presenter-lapel": { "mute": false, "level_db": -6.2, "gain_db": 32.0 },
      "podium":          { "mute": true,  "level_db": -8.0, "gain_db": 28.0 }
    },
    "dca": {
      "non-choir": { "mute": false, "level_db": 0.0 },
      "choir":     { "mute": true,  "level_db": -3.0 }
    }
  },
  "camera": {
    "main": { "preset": "choir", "pan": -0.25, "tilt": 0.10, "zoom": 0.40 }
  },
  "obs": {
    "scene": "camera-with-slides",
    "streaming": true,
    "recording": true,
    "uptime_seconds": 2535
  },
  "slides": {
    "current": 5,
    "total": 24,
    "title": "Closing Hymn",
    "pending_actions": []
  },
  "stream": {
    "platform": "restream",
    "viewers": 12,
    "chat": {
      "provider": "restream",
      "status": "ready"
    }
  }
}
```

`rev` is a monotonically increasing revision number assigned by the server. It increments on every state change. Clients use it to order updates and detect dropped frames. Every `state` snapshot (including those returned by `get_state` or a subscription change) carries the current `rev`; clients resume gap detection from that value.

Camera `pan`/`tilt` are absolute normalized positions in −1.0..1.0 and `zoom` in 0.0..1.0 — the same scale as the `position` command (see [§5](#5-actions-catalog)), so a client can read state and command the camera back to it. The server maps these to/from device-native units (e.g. VISCA raw) per camera configuration. In single-camera deployments the lone camera is keyed `main` (as in the example above), so a client that omits `camera_id` on its commands reads and writes that one camera; multi-camera deployments key each camera by its `camera_id`.

`stream.chat` describes the chat panel (added in `1.1`). `status` is `ready` when the server can produce a chat URL, or `needs_auth` when an operator must authorize the platform first — the client shows a connect prompt and sends them to `/chat/auth/start` (see [§11](#11-chat-endpoints)). The whole `chat` object is **absent** when no chat provider is configured, which a client displays differently from a provider that is merely unauthorized. The chat URL itself is deliberately not carried here: it embeds a credential and would be re-broadcast to every subscriber each time the server refreshed it, so clients fetch one from `/chat/url` when they need it.

`preset` holds the name of the last recalled preset. Once a subsequent `position`, `pan_tilt`, or `zoom` command moves the camera off it, the server sets `preset` to `""` (empty string = no active preset). It is never set to `null` — `null` is delete-only under the delta rules (see [`state-delta`](#state-delta--partial-update)), so the empty string is the off-preset sentinel.

### `state-delta` — partial update

Sent on each state change. Payload is a sparse JSON-Merge-Patch-style object: only fields that changed.

```json
{
  "type": "state-delta",
  "rev": 143,
  "patch": {
    "audio": {
      "channels": {
        "presenter-lapel": { "mute": true }
      }
    }
  }
}
```

Apply rules:
- Object values are merged recursively.
- `null` removes the key.
- Arrays are replaced wholesale.
- Because `null` is reserved for deletion, no field is ever *set* to a literal JSON `null`; the state model has no null-valued fields by design.

If a client observes a `rev` gap (e.g. `rev=143` arrives after `rev=141` with no `142`), it MUST request a re-sync by sending `get_state`, which returns a fresh `state` for the current subscription.

### `ack` / `nak` — command result

Confirms a `cmd` was accepted (`ack`) or rejected (`nak`). An `ack` is sent before the resulting `state-delta`, **if any** — some accepted commands produce no modeled state change and are never followed by a delta.

```json
{ "type": "ack", "id": "c123" }
{ "type": "nak", "id": "c124", "error": { "code": "unknown_preset", "message": "no camera preset named 'choir-stage-left'" } }
```

`nak` does not produce a `state-delta`.

### `pong`

```json
{ "type": "pong", "id": "k42" }
```

### `event` — out-of-band notifications

For things that aren't state changes but the operator should see: feedback detections, automation overrides, connection issues with hardware, etc.

```json
{
  "type": "event",
  "id": "e567",
  "severity": "warn",
  "source": "audio.feedback",
  "message": "Suppressed feedback on presenter-lapel (1.8 kHz)",
  "data": { "channel": "presenter-lapel", "frequency_hz": 1800, "action": "mute" }
}
```

`severity` is one of `info`, `warn`, `error`. Events are advisory; the resulting state changes (if any) come through `state-delta` separately.

`id` is a server-assigned unique event identifier. Events are not acked, but clients MAY use `id` to de-duplicate (e.g. across a reconnect) and to correlate an event with server logs.

### `error` — protocol-level error

Sent when the client violated the protocol (e.g. sent a `cmd` before `hello`, or referenced an unknown topic). Distinct from `nak`, which is for command-level rejections.

```json
{
  "type": "error",
  "code": "protocol",
  "message": "cmd received before hello"
}
```

After sending `error`, the server MAY close the connection.

---

## 5. Actions catalog

Per-`target` action names and `value` shapes. This list grows as phases land; v1.0 ships with the subset marked **(v1)**.

Where a row lists `value: none`, the `value` field MUST be omitted from the `cmd`; servers MUST also accept an explicit `null` as equivalent. (`none` is shorthand for "no payload", not a JSON value.)

### `target: camera`

| `action` | `value` | Notes |
|---|---|---|
| `preset` | string | **(v1)** Recall a named preset. |
| `position` | `{ pan?: -1.0..1.0, tilt?: -1.0..1.0, zoom?: 0.0..1.0 }` | **(v1)** **Absolute** move to a normalized position. Any subset of axes may be given. Same scale as `state.camera.<id>`, so a client can read state and command back to it. |
| `pan_tilt` | `{ pan: -1.0..1.0, tilt: -1.0..1.0 }` | **Velocity** (rate), not position. Continuous joystick input; each frame replaces the previous. `{pan:0,tilt:0}` is stop. |
| `zoom` | float `-1.0..1.0` | **Velocity** (rate). Continuous zoom; positive = tele, negative = wide. `0` is stop. |

`pan_tilt` and `zoom` carry **velocity** for smooth joystick control and SHOULD be sent at 30–60 Hz while the joystick/slider is active, with a final `0` on release. `position` carries an **absolute** normalized target and is how a client returns the camera to a known spot reliably (velocity moves can't). The two are distinct actions even though `pan_tilt` and `position` share the `pan`/`tilt` value range — one is a rate, the other a target. State reports absolute position (see §4); the server maps normalized values to/from device-native units per camera config. `zoom` carries the same hazard under a single name: the standalone `zoom` action is a **velocity** in −1.0..1.0 (positive = tele, negative = wide), while `position.zoom` is an **absolute** target in 0.0..1.0 — same field name, different range and meaning, so don't conflate them.

During a continuous move the server MUST NOT emit a `state-delta` per velocity-input frame. It coalesces camera position updates and reports `camera.<id>` `pan`/`tilt`/`zoom` at a bounded rate (≤10 Hz suggested) plus a final delta once motion settles. Position state is therefore eventually-consistent while a move is in progress and authoritative once it settles — `/ws` is never driven at the 30–60 Hz velocity-input rate (the same flooding concern that puts meters on their own `/ws/meters` endpoint, see §6).

> **Implementation phasing.** `(v1)` marks an action as part of the v1.0 *protocol* — it does not mean every server implements it from day one. `preset` routes through Bitfocus Companion and ships in the server's Phase 1. `position`, `pan_tilt`, and `zoom` require direct VISCA control and land in Phase 3 (CB-030+). A server that has not yet implemented a recognized camera action rejects it with a `device_unavailable` nak (distinct from `unknown_action`, which is for an unrecognized action name).

### `target: audio`

| `action` | `value` | Notes |
|---|---|---|
| `set_mute` | `{ id: "<channel-or-dca>", mute: bool }` | **(v1)** |
| `set_fader` | `{ id: string, level_db: float }` | Continuous OK; ≤30 Hz suggested. |
| `set_gain` | `{ id: string, gain_db: float }` | |
| `apply_profile` | `{ id: string, profile: string }` | |
| `dca_member` | `{ dca: string, channel: string, member: bool }` | Manage DCA membership (rare). |

Across audio actions `id` is the channel-or-DCA identifier (same meaning as in `set_mute`). Channel and DCA identifiers share a single namespace — a deployment MUST NOT reuse a name for both — so `id` resolves unambiguously against channels and DCAs together. `dca_member` is the intentional exception to the single-`id` shape: it names two distinct roles — `dca` (the group) and `channel` (the member being added or removed). Not every audio action applies to both kinds of `id`: `set_gain` is **channel-only**, since DCAs expose just `{ mute, level_db }` in the state model (no `gain_db`) — a `set_gain` whose `id` resolves to a DCA is invalid and MUST be `nak`'d with `invalid_target_kind` (see §8). DCAs accept `set_mute`, `set_fader`, and `dca_member`; channels accept every audio action.

### `target: scene`

| `action` | `value` | Notes |
|---|---|---|
| `set` | string | **(v1)** Switch to the named scene preset. |

### `target: slides`

| `action` | `value` | Notes |
|---|---|---|
| `next` | none | **(v1)** Advance one slide. |
| `prev` | none | **(v1)** |
| `confirm_pending` | none | Apply the pending `apply: on-confirm` rule actions. |
| `cancel_pending` | none | Discard them. |

### `target: streaming` / `target: recording`

| `action` | `value` | Notes |
|---|---|---|
| `start` | none | **(v1)** |
| `stop` | none | **(v1)** |

### `target: power`

| `action` | `value` | Notes |
|---|---|---|
| `on` / `off` | `{ id: "<plug-id>" }` | Lands with CB-080. |
| `run_sequence` | `{ name: "pre-event" \| "post-event" }` | CB-081 / CB-082. |

### `target: automation`

| `action` | `value` | Notes |
|---|---|---|
| `set_enabled` | `{ feature: string, enabled: bool }` | Per-feature override. Features: `feedback-suppression`, `auto-level`, `vad-mute`. |

---

## 6. Meter channel

A separate WebSocket at `/ws/meters` carries high-frequency meter data so the main channel isn't flooded.

- No `hello`. The connection is immediately ready.
- Server pushes one `meters` frame per cadence period (default 10 Hz; configurable per deployment).
- Frame size is bounded by visible channel count from server config.
- Backpressure: server MAY drop frames if the socket buffer is full. Clients should not assume contiguous frames.

```json
{
  "type": "meters",
  "ts_ms": 1234567890123,
  "channels": {
    "presenter-lapel": { "peak_db": -12.3, "rms_db": -18.4 },
    "podium":          { "peak_db": -60.0, "rms_db": -60.0 },
    "choir":           { "peak_db":  -3.1, "rms_db":  -9.7 }
  },
  "buses": {
    "stream":   { "peak_db": -8.2, "rms_db": -14.1 },
    "main_lr":  { "peak_db": -7.0, "rms_db": -13.4 }
  }
}
```

Values are dBFS. Channels/buses present in the frame are exactly those marked visible by server config (CB-024).

The `channels` map keys are audio identifiers from the shared channel/DCA namespace, so a metered point may be a physical channel *or* a DCA (e.g. `choir` above is a DCA in the `state` model, not a channel). `buses` are output mix buses (e.g. the stream bus and main L/R) that are metered but not individually represented in the v1 `state` model.

`ts_ms` is the server's wall-clock time in Unix epoch milliseconds (UTC) at the moment the frame was sampled. It is advisory — useful for ordering and for correlating meter frames with logged events — and is not a monotonic clock, so it MAY jump on NTP adjustment. Clients MUST NOT assume a fixed interval between successive `ts_ms` values (see backpressure above).

---

## 7. Reserved / forward compatibility

- Frames with unknown `type` MUST be ignored, not error.
- Fields with unknown names inside known `type`s MUST be ignored.
- Servers MUST NOT change the meaning of an existing field within a major version; only add new fields with defaults.
- Clients SHOULD treat all numeric fields with care: integers may appear as JSON numbers without decimal, but clients SHOULD accept either.

---

## 8. Error codes

Strings used in `nak.error.code` and `error.code`. Open-ended — implementations MAY define new ones (lowercased, snake_case).

| Code | Meaning |
|---|---|
| `protocol` | Frame violated the wire protocol (wrong order, malformed envelope) |
| `unknown_target` | `cmd.target` not recognized |
| `unknown_action` | `cmd.target` is recognized but `cmd.action` is not |
| `unknown_topic` | `subscribe`/`unsubscribe` named a topic not in the valid set |
| `unknown_preset` | Referenced preset name not in server config |
| `unknown_channel` | Referenced audio channel/DCA not in server config |
| `invalid_target_kind` | `id` exists but is the wrong kind for the action (e.g. `set_gain` targeting a DCA, which has no `gain_db`) |
| `device_unavailable` | Downstream device (mixer, camera, OBS, Companion) not reachable |
| `permission_denied` | Action not permitted in current context (e.g., automation override locked out) |
| `internal` | Server-side error not otherwise classified |

---

## 9. Open items (deferred to later versions)

- Per-client auth tokens
- Field-level access control (e.g., read-only viewer clients)
- Compression for `meters` frames at high client counts
- Binary frames for screenshot/video preview (currently planned over the main `/ws` channel using base64 JSON — see CB-061)
- Observable state for the `power` / `automation` targets — e.g. reading back `automation set_enabled` toggles so multi-client UIs can reconcile them. These targets are advisory in v1 with no state key.

---

## 10. Companion surface channel

The primary operator control surface is the **Companion Satellite surface**: the server registers itself with Bitfocus Companion's Satellite API as a remote surface (the same mechanism a Stream Deck Satellite or Companion's web emulator uses), and Companion renders each configured button to a bitmap and streams it to the server. The server forwards those bitmaps to clients, which display them natively, and routes presses back to Companion.

The design intent: the button grid is **whatever Companion is configured with** — labels, icons, colors, page navigation, and live feedback are all rendered by Companion. There is nothing to define or maintain client-side, and the grid can never drift out of sync with the Companion configuration. This replaces any client-defined button vocabulary.

Surface frames travel on the main `/ws` channel but are **not** part of the `state`/`state-delta` machinery and are **not** a subscription topic: button bitmaps are large and change frequently (clocks, feedback), so routing them through revisioned state deltas would be wasteful. Every client receives the surface unconditionally, after the initial `state` snapshot.

Otherwise the two are independent streams. A client MUST NOT assume a surface frame and a state frame arrive in the order the server produced them: when a connection is behind, the server sends every non-surface frame it has pending ahead of queued button images, so that tapping a control is acknowledged promptly on a link too slow to carry a re-rendered page. Ordering *within* each stream is unaffected.

### `surface-layout` (server → client)

Announces the surface grid dimensions. Sent after the initial `state` snapshot, again whenever the server's surface (re)registers with Companion (e.g. after a reconnect), and again after a `subscribe`/`unsubscribe`, which re-sends the surface so a topic change can't cost a client the keys broadcast while it was re-baselining. A client treats each `surface-layout` as a re-baseline: it resets its grid to these dimensions and drops the keys the layout supersedes, since the server re-sends every key afterward.

```json
{ "type": "surface-layout", "rows": 4, "cols": 8, "seq": 141, "bitmap_size": 72 }
```

| Field | Type | Notes |
|---|---|---|
| `rows` / `cols` | int | Grid dimensions. The flat key index is `row * cols + col`. |
| `seq` | int | The surface sequence this layout was taken at. Drop held keys whose last applied `seq` is **≤** this value; keep anything newer. |
| `bitmap_size` | int | Button bitmap edge length in pixels (square). Always positive (the server normalizes it to a default). |

A client must not drop keys newer than `seq`. A `surface-key` above the layout's sequence is a render the layout does not cover, and discarding it would leave the button showing a state Companion has already moved on from.

### `surface-key` (server → client)

One key's current rendered state. Sent for every cached key right after `surface-layout` on connect, and on every change Companion pushes.

```json
{
  "type": "surface-key",
  "key": 9,
  "seq": 142,
  "row": 1,
  "col": 1,
  "key_type": "BUTTON",
  "pressed": false,
  "color": "#1b3a5c",
  "bitmap": "<base64 RGB>"
}
```

| Field | Type | Notes |
|---|---|---|
| `key` | int | Flat key index (`row * cols + col`). |
| `seq` | int | Monotonically increasing surface-update sequence. See ordering below. |
| `row` / `col` | int | Grid position (derived from `key`, included for convenience). |
| `key_type` | string | `BUTTON`, `PAGEUP`, `PAGEDOWN`, or `PAGENUM`. Navigation types may carry no bitmap; the client renders an affordance. |
| `pressed` | bool | Current pressed state from Companion feedback. |
| `color` | string | Background color `#rrggbb` — a fallback shown until/unless a bitmap is available. Omitted if none. |
| `bitmap` | string | Companion's rendered button image: base64-encoded 8-bit RGB pixel data, `bitmap_size`×`bitmap_size`, forwarded verbatim. Omitted if none. |

**Ordering.** The cached `surface-key` frames sent on connect can race a concurrent live update for the same key. Clients MUST apply updates per key in `seq` order — last-write-wins — and ignore any frame whose `seq` is not newer than the last applied for that key. `seq` is a single surface-wide counter, not per-key.

A `surface-key` can therefore arrive before the first `surface-layout`, when a live update overtakes the replay a connecting client is about to be sent. Its `seq` is never above that layout's, so the layout supersedes it: a client that has not yet sized its grid may discard such a frame outright rather than hold a key whose bitmap it cannot yet decode.

**Coalescing.** `seq` increases but is **not** contiguous: a client MUST NOT assume it will see every value, or treat a gap as a lost frame. When a client is not draining as fast as Companion renders, the server replaces the frame it still has queued for a key with the newer one rather than sending both, and drops the frames a `surface-layout` supersedes. What arrives is always the newest render of each key, which is the only one a last-write-wins client would have kept, and the surface backlog cannot outgrow one frame per key.

### `surface-press` (client → server)

Presses (or releases) a surface key. A normal tap is a press (`true`) immediately followed by a release (`false`), mirroring a physical button. Like `cmd`, clients MUST NOT send it before `hello`. It is not `ack`'d; a failure (e.g. the server's Companion connection is down) surfaces as a warn `event`.

```json
{ "type": "surface-press", "key": 9, "pressed": true }
```

> **Implementation note.** The server speaks the Satellite protocol over TCP (Companion's default port 16622). The surface grid defaults to a Stream Deck XL layout (8 columns × 4 rows, 72px bitmaps), configurable per deployment, and is disabled if no satellite endpoint is configured.

---

## 11. Chat endpoints

Chat is the one part of the client↔server surface that is plain HTTP rather than WebSocket frames. Both ends of the platform's OAuth handshake are browser navigations, and the minted chat URL embeds a credential that has no business being broadcast to every subscriber in a state snapshot.

**The server holds the platform credential; the client never sees one.** Restream's OAuth offers no PKCE and its token exchange requires a client secret, which an application distributed to operators cannot keep. Restream's own documentation directs integrations to keep that secret off user devices and to refresh through a proxy the application provides — the server is that proxy.

These routes exist only when a chat provider is configured. A deployment without one answers `404`, rather than exposing endpoints that could only fail.

### `GET /chat/url`

Mints a chat URL for the client to display. Clients call this each time they need one and MUST NOT cache it: the token inside is the platform's to expire, and minting another is a server-side token refresh rather than an operator re-authorizing.

`200` — a URL to display:

```json
{ "url": "https://chat.restream.io/embed?token=..." }
```

`409` — the provider holds no usable credential:

```json
{ "status": "needs_auth", "auth_start": "/chat/auth/start" }
```

The response names the *path* that begins authorization rather than a ready-made login URL, because minting one creates server-side state; a client polling an unauthorized server would otherwise leave a pending authorization behind on every request.

`502` — the provider is authorized but the platform did not answer.

### `GET /chat/auth/start`

Redirects (`302`) the operator's browser to the platform's authorize dialog, carrying a single-use `state` parameter that the callback must echo back.

### `GET /chat/auth/callback`

Where the platform redirects once the operator approves or declines. It renders an HTML page, not JSON, because a browser lands on it.

On success the server persists the credential and republishes `stream.chat.status` as `ready`, so a client showing the connect prompt switches over without reconnecting. A callback carrying no `code` parameter is how the platform reports that the operator declined.

> **Implementation note.** Restream rotates the refresh token on every use — the previous pair is invalidated the moment a refresh succeeds — so the server persists each newly issued pair immediately and serializes refreshes. The stored credential is state, not configuration: losing the file costs an operator one re-authorization.

### Not in this protocol: sending messages

Chat is display-only. Restream's chat WebSocket is documented as one-directional ("the server will ignore any incoming messages") and their REST surface exposes no send endpoint — the `reply_*` and `relay_*` actions report replies composed in Restream's own app rather than accepting them. Posting a message is therefore only possible inside the embedded chat UI itself, so CueBooth does not offer a send or canned-message command.
