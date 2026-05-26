# CueBooth Client ↔ Server Protocol

**Version:** v1 (draft)
**Transport:** WebSocket, JSON text frames
**Endpoint:** `/ws` on the server's HTTP listener
**Meter endpoint:** `/ws/meters` (see [Meter channel](#meter-channel))

This document is the normative spec for the wire protocol between a CueBooth client (typically the Flutter app) and the cuebooth-server (the Go orchestrator). Server and client implementations should be developed against this spec rather than against each other.

The design rationale is in [design.md](design.md) §3.5. This document fills in the details that §3.5 only sketches.

---

## 1. Connection lifecycle

1. Client opens a WebSocket to `ws://<host>:<port>/ws`.
2. Server immediately sends a `hello` frame. Clients MUST receive a `hello` before sending any commands; servers MUST send it within 500 ms of accepting the socket.
3. Client opens a *second* WebSocket to `/ws/meters` if it wants high-rate meter data. This is independent of `/ws` — it has its own lifecycle, no `hello`, and only carries meter frames.
4. Either side may close at any time. Clients reconnect with exponential backoff (1s → 30s cap) is the recommended pattern.

### Authentication

v1 has no in-protocol auth. Deployments rely on network-level isolation (LAN + Tailscale per [design.md](design.md) §3.6). A future revision will add a token handshake; that's out of scope for v1.

### Versioning

The `hello` frame carries a `proto` field naming the protocol version. Clients MUST refuse to operate against a server whose `proto` differs in major version. Minor-version bumps are additive and backwards-compatible (new optional fields, new `type` values clients can safely ignore).

```json
{
  "type": "hello",
  "proto": "1.0",
  "server_version": "0.1.0",
  "server_id": "production-pc"
}
```

When v2 lands, servers may opt to maintain v1 compatibility by feature-detecting the client. Clients should not assume v1 servers will ever be retrofitted with v2.

---

## 2. Envelope

Every frame is a single JSON object with a `type` field that determines the rest of the shape.

```json
{ "type": "<message-type>", ... }
```

Unknown `type` values MUST be ignored (forwards compatibility). Malformed JSON MUST result in a connection close (code 1003, "Unsupported Data"). Servers SHOULD log such events for debugging.

Field naming convention: `snake_case`.

---

## 3. Client → Server messages

### `cmd` — execute an action

A client request to mutate state. The server executes the action (via Companion, OSC, VISCA, etc. as appropriate) and the resulting state is broadcast in the next `state` or `state-delta` frame.

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
| `target` | string | yes | One of: `camera`, `audio`, `scene`, `slide`, `stream`, `recording`, `power`, `automation`. Other targets MAY be added in minor versions. |
| `action` | string | yes | Per-target verb; see [§5 Actions catalog](#5-actions-catalog). |
| `value` | any | depends | Per-action payload. May be string, number, bool, or object. |
| `camera_id` | string | depends | Required for `target: camera` in multi-camera deployments. Optional and ignored in single-camera setups. |

### `subscribe` / `unsubscribe`

Opt in or out of state-update streams. v1 supports subscribing to topics; the default subscription is everything except meters (which require the `/ws/meters` endpoint).

```json
{ "type": "subscribe",   "topics": ["audio", "camera", "obs", "slides"] }
{ "type": "unsubscribe", "topics": ["slides"] }
```

If a client never sends `subscribe`, it is implicitly subscribed to all non-meter topics.

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

Sent once after `hello`, and again any time a client `subscribe`s or after a server-side reset.

```json
{
  "type": "state",
  "rev": 142,
  "audio": {
    "channels": {
      "presenter-lapel": { "mute": false, "fader": -6.2, "gain": 32 },
      "podium":          { "mute": true,  "fader": -8.0, "gain": 28 }
    },
    "dca": {
      "non-presenter": { "mute": false, "fader": 0.0 },
      "choir":         { "mute": true,  "fader": -3.0 }
    }
  },
  "camera": {
    "main": { "preset": "choir", "pan": 128, "tilt": 45, "zoom": 200 }
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
    "viewers": 12
  }
}
```

`rev` is a monotonically increasing revision number assigned by the server. It increments on every state change. Clients use it to order updates and detect dropped frames.

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

If a client observes a `rev` gap (e.g. `rev=143` arrives after `rev=141` with no `142`), it MUST request a re-sync via `subscribe` (which returns a fresh `state`).

### `ack` / `nak` — command result

Confirms a `cmd` was accepted (`ack`) or rejected (`nak`). Sent before the resulting `state-delta`.

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

### `target: camera`

| `action` | `value` | Notes |
|---|---|---|
| `preset` | string | **(v1)** Recall a named preset. |
| `pan_tilt` | `{ pan: -1.0..1.0, tilt: -1.0..1.0 }` | Continuous joystick input. Each frame replaces the previous. `{pan:0,tilt:0}` is stop. |
| `zoom` | float `-1.0..1.0` | Continuous zoom; positive = tele, negative = wide. `0` is stop. |

`pan_tilt` and `zoom` SHOULD be sent at 30–60 Hz while the joystick/slider is active and a final `0` on release.

### `target: audio`

| `action` | `value` | Notes |
|---|---|---|
| `set_mute` | `{ id: "<channel-or-dca>", mute: bool }` | **(v1)** |
| `set_fader` | `{ id: string, level_db: float }` | Continuous OK; ≤30 Hz suggested. |
| `set_gain` | `{ id: string, gain_db: float }` | |
| `apply_profile` | `{ channel: string, profile: string }` | |
| `dca_member` | `{ dca: string, channel: string, member: bool }` | Manage DCA membership (rare). |

### `target: scene`

| `action` | `value` | Notes |
|---|---|---|
| `set` | string | **(v1)** Switch to the named scene preset. |

### `target: slide`

| `action` | `value` | Notes |
|---|---|---|
| `next` | none | **(v1)** Advance one slide. |
| `prev` | none | **(v1)** |
| `confirm_pending` | none | Drain queued `apply: on-confirm` rule actions. |
| `cancel_pending` | none | Discard them. |

### `target: stream` / `target: recording`

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
| `unknown_preset` | Referenced preset name not in server config |
| `unknown_channel` | Referenced audio channel/DCA not in server config |
| `device_unavailable` | Downstream device (mixer, camera, OBS, Companion) not reachable |
| `permission_denied` | Action not permitted in current context (e.g., automation override locked out) |
| `internal` | Server-side error not otherwise classified |

---

## 9. Open items (deferred to later versions)

- Per-client auth tokens
- Field-level access control (e.g., read-only viewer clients)
- Compression for `meters` frames at high client counts
- Binary frames for screenshot/video preview (currently planned over the main `/ws` channel using base64 JSON — see CB-061)
