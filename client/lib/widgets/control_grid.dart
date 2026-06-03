import 'package:flutter/material.dart';

import '../services/app_state.dart';
import '../services/protocol.dart';
import '../services/session.dart';

/// What a control button does and how its active state is derived.
enum ControlKind {
  /// Recall an OBS scene preset (`scene`/`set`); active when it's the current scene.
  scene,

  /// Recall a camera preset (`camera`/`preset`); active when it's the current preset.
  cameraPreset,

  /// Mute or unmute an audio channel/DCA (`audio`/`set_mute`). Audio state is
  /// not modeled until Phase 2, so these don't reflect an active state yet.
  audioMute,
}

/// A single control on the grid. The label is what the operator sees; the rest
/// is the command it issues (protocol.md §5).
class ControlButton {
  const ControlButton({
    required this.label,
    required this.kind,
    required this.name,
    this.cameraId,
    this.mute,
  });

  final String label;
  final ControlKind kind;

  /// The preset/scene name, or the audio channel id for [ControlKind.audioMute].
  final String name;

  /// Camera id for [ControlKind.cameraPreset] (defaults to "main").
  final String? cameraId;

  /// For [ControlKind.audioMute]: true to mute, false to unmute.
  final bool? mute;

  /// Whether this button represents the current server state.
  bool isActive(AppState state) {
    switch (kind) {
      case ControlKind.scene:
        return state.obsScene == name;
      case ControlKind.cameraPreset:
        return state.cameraPreset(cameraId ?? 'main') == name;
      case ControlKind.audioMute:
        return false; // no audio state in v1
    }
  }

  /// Issues this button's command on [session].
  void dispatch(Session session) {
    switch (kind) {
      case ControlKind.scene:
        session.sendCommand(Target.scene, 'set', value: name);
      case ControlKind.cameraPreset:
        session.sendCommand(
          Target.camera,
          'preset',
          value: name,
          cameraId: cameraId,
        );
      case ControlKind.audioMute:
        session.sendCommand(
          Target.audio,
          'set_mute',
          value: {'id': name, 'mute': mute ?? true},
        );
    }
  }
}

/// A labelled group of control buttons.
class ControlSection {
  const ControlSection(this.title, this.buttons);
  final String title;
  final List<ControlButton> buttons;
}

/// The default control layout. This is intentionally simple, deployment-flavored
/// seed data (matching docs/sample-deployment.md) meant to be edited per
/// install — v1 has no protocol way to learn a server's configured preset
/// vocabulary, so the grid is defined here rather than discovered. A later
/// revision could source it from server config or a settings screen.
List<ControlSection> defaultControlSections() => const [
  ControlSection('Scenes', [
    ControlButton(label: 'Beginning', kind: ControlKind.scene, name: 'beginning'),
    ControlButton(label: 'Camera', kind: ControlKind.scene, name: 'camera-only'),
    ControlButton(
      label: 'Camera + Slides',
      kind: ControlKind.scene,
      name: 'camera-with-slides',
    ),
    ControlButton(label: 'Slides', kind: ControlKind.scene, name: 'slides-only'),
  ]),
  ControlSection('Camera', [
    ControlButton(label: 'Podium', kind: ControlKind.cameraPreset, name: 'podium'),
    ControlButton(label: 'Piano', kind: ControlKind.cameraPreset, name: 'piano'),
    ControlButton(label: 'Choir', kind: ControlKind.cameraPreset, name: 'choir'),
    ControlButton(
      label: 'Sanctuary',
      kind: ControlKind.cameraPreset,
      name: 'sanctuary-wide',
    ),
  ]),
  ControlSection('Audio', [
    ControlButton(
      label: 'Mute non-choir',
      kind: ControlKind.audioMute,
      name: 'non-choir',
      mute: true,
    ),
    ControlButton(
      label: 'Unmute non-choir',
      kind: ControlKind.audioMute,
      name: 'non-choir',
      mute: false,
    ),
    ControlButton(
      label: 'Mute choir',
      kind: ControlKind.audioMute,
      name: 'choir',
      mute: true,
    ),
    ControlButton(
      label: 'Unmute choir',
      kind: ControlKind.audioMute,
      name: 'choir',
      mute: false,
    ),
  ]),
];

/// The operator button grid: scene/camera/audio controls mapped to Companion
/// actions via the server. Buttons send commands and reflect server state
/// (the active scene/preset is highlighted). Rebuilds on state changes.
class ControlGrid extends StatelessWidget {
  const ControlGrid({super.key, required this.session, this.sections});

  final Session session;

  /// Override the control layout (defaults to [defaultControlSections]).
  final List<ControlSection>? sections;

  @override
  Widget build(BuildContext context) {
    final layout = sections ?? defaultControlSections();
    return ListenableBuilder(
      listenable: session.state,
      builder: (context, _) {
        return ListView(
          padding: const EdgeInsets.all(16),
          children: [
            for (final section in layout) ...[
              Padding(
                padding: const EdgeInsets.only(top: 8, bottom: 8),
                child: Text(
                  section.title,
                  style: Theme.of(context).textTheme.titleMedium,
                ),
              ),
              Wrap(
                spacing: 8,
                runSpacing: 8,
                children: [
                  for (final button in section.buttons)
                    _ControlChip(
                      button: button,
                      active: button.isActive(session.state),
                      onPressed: () => button.dispatch(session),
                    ),
                ],
              ),
            ],
          ],
        );
      },
    );
  }
}

class _ControlChip extends StatelessWidget {
  const _ControlChip({
    required this.button,
    required this.active,
    required this.onPressed,
  });

  final ControlButton button;
  final bool active;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) {
    // Active controls are filled; the rest are outlined — a clear at-a-glance
    // "what's live now" without color theory. Easy to restyle later.
    return active
        ? FilledButton(onPressed: onPressed, child: Text(button.label))
        : OutlinedButton(onPressed: onPressed, child: Text(button.label));
  }
}
