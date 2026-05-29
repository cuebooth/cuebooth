# Slide Rules — Authoring Guide

This guide is for the person who builds the slide deck for a CueBooth-driven event.

CueBooth watches the active PowerPoint presentation. On every slide change it reads that slide's notes, looks for a `@cuebooth` block, and executes the rules it finds — either immediately, or held as a pending action set for the operator to confirm with a clicker press.

You write rules using a small, line-oriented DSL inside slide notes. The names you reference (`choir`, `podium`, `camera-with-slides`, `non-choir`) are **logical preset names** defined once in the server's config for your deployment — you don't need to know IPs, OSC paths, or Companion button coordinates.

This guide covers the DSL. Ask your operator for the list of preset names available in your deployment.

---

## Where rules go

Open the **Notes** pane below a slide in PowerPoint (View → Notes). Add a `@cuebooth` block anywhere in the notes. Everything before it (or after a blank line following the block) is treated as ordinary notes and ignored.

```
This is the offertory hymn. The hymn number is in the projected slide.

@cuebooth
camera.main: choir
scene: camera-with-slides
audio.mute: non-choir
audio.unmute: choir
apply: immediate
```

That's it. When the slideshow advances to this slide, CueBooth will recall the `choir` camera preset, switch the OBS scene to camera-with-slides, mute the non-choir DCA group, and unmute the choir.

---

## Rule keys

Keys are case-insensitive. One key per line. Values are trimmed of surrounding whitespace, but are **case-sensitive** (`choir` is not the same preset as `Choir`).

### `camera.<id>: <preset-name>`

Recalls a named camera preset on a specific camera, identified by `<id>` (defined in config). The preset typically maps to a Companion button that drives a VISCA preset recall.

```
camera.main: podium
```

Always include the camera ID — even single-camera deployments name their one camera (e.g. `main`). That way a deck keeps working unchanged if a second camera is added later.

For a multi-camera deployment, target each one by its ID:

```
camera.main: podium
camera.front: wide
```

### `scene: <scene-preset-name>`

Switches the OBS scene. The value is a logical scene preset name from the config — not the raw OBS scene name. The config maps it to either a Companion button or a direct OBS-WebSocket call.

```
scene: camera-only
```

### `audio.mute: <preset>[, <preset>, ...]`

Mutes one or more channels, DCA groups, or named groups. Values can be a single preset name or a comma-separated list.

```
audio.mute: non-choir
audio.mute: choir, podium, presenter-lapel
```

### `audio.unmute: <preset>[, <preset>, ...]`

Same shape as `audio.mute`, but unmutes.

```
audio.unmute: choir
```

### `apply: immediate | on-confirm`

Controls *when* the actions on this slide are executed.

- `apply: immediate` (default if omitted) — actions run the moment the slide becomes active.
- `apply: on-confirm` — actions become the slide's *pending* set instead of running right away. The operator sees them in the control surface and applies them by pressing the confirm button on the clicker (long-press forward, by default) or tapping the confirm button in the client.

Use `on-confirm` when the slide should change *visually* ahead of the audio/camera change. The canonical example: a reader is announcing the next hymn while still on the current slide's mic, and you want the next hymn's slide to appear before muting the reader.

```
camera.main: choir
scene: camera-with-slides
audio.mute: podium
audio.unmute: choir
apply: on-confirm
```

When the operator presses confirm, all four actions fire at once.

---

## Pending action behavior

When a slide's `apply: on-confirm` actions are pending, the operator's view shows what's waiting. There is only ever **one** pending set at a time — the one from the slide you're currently on. Confirm (long-press forward, by default) applies it; cancel (long-press back) discards it.

Because of that, **confirm while you're still on the slide.** If you advance to another slide first, the pending set is *not* applied — it's replaced by whatever the new slide defines (a new pending set, immediate actions, or nothing). This is deliberate: it stops a run of un-confirmed slides from piling up and then firing a minute's worth of camera moves and mic changes all at once.

If a slide should trigger no automation, omit the rule block.

---

## Examples by event segment

These examples reuse the logical preset names introduced earlier in this guide; substitute your deployment's names.

### Opening: pianist plays prelude

```
@cuebooth
camera.main: piano
scene: camera-with-slides
audio.mute: presenter, podium, choir
apply: immediate
```

### Reading at the podium

```
@cuebooth
camera.main: podium-with-slides
scene: camera-with-slides
audio.unmute: podium
audio.mute: presenter
apply: immediate
```

### Choir piece

```
@cuebooth
camera.main: choir
scene: camera-with-slides
audio.mute: non-choir
audio.unmute: choir
apply: immediate
```

### Slide changes during the sermon, but the speaker doesn't move

```
@cuebooth
```

(A truly empty rule block — explicit confirmation that no automation is intended for this slide. Equivalent to omitting the block entirely; some authors prefer the explicit form.)

### Transition into a song, deferring the audio change

The reader is announcing the upcoming song while the music director moves to the piano. You want the song slide to appear during the announcement, but you don't want the reader's mic muted yet.

```
@cuebooth
camera.main: piano
scene: camera-with-slides
audio.mute: podium
audio.unmute: choir
apply: on-confirm
```

The operator (or the clicker's confirm button) fires the audio + camera change at the right moment.

---

## Discovering preset names in your deployment

The server's TOML config (typically `cuebooth.toml`) defines all preset names available. Look for sections like:

```toml
[presets.camera.choir]
[presets.camera.podium]
[presets.scene.camera-with-slides]
[presets.audio.mute.non-choir]
```

Ask your operator for the canonical list, or for read access to the config file. A future addition (tracked in CB-044) is a `cuebooth-server list-presets` command that prints the available names.

---

## Validation

The server checks rule blocks when it parses the deck, logging a warning for anything it can't resolve (such as a preset name that isn't defined in the server config). Later, when the slide actually becomes active, any unresolved action is skipped while the rest of the slide's actions still fire, and the operator sees the warning in the client. This is intentional: a typo in one action shouldn't break the whole transition.

For deck-wide validation before an event, the planned `cuebooth-server check-deck <path-to-pptx>` command will list every unrecognized preset name across the whole deck.

---

## Conventions and tips

- **Keep blocks short.** If a slide needs more than ~6 lines of rules, consider whether the deployment's preset names are too granular.
- **Name presets by intent, not by hardware.** `presenter` is better than `ch3-lapel`; the config maps intent to hardware.
- **Use `on-confirm` sparingly.** It adds operator load. Reserve it for cases where slide-timing genuinely should lead audio/camera-timing.
- **Empty rule blocks are valid.** They communicate "this slide is intentionally no-op" to anyone reading the deck later.
- **The block is plain text in notes.** Anyone with PowerPoint can read and edit it; no special tooling required.

---

## See also

- [Design document](design.md) §3.4 "Slide Rule Format" — the formal DSL definition and the routing logic that turns rules into actions
- Server config reference (lands with CB-011 + CB-044) — preset name → Companion button / OSC command mappings
