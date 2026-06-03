import 'package:flutter/material.dart';

import '../services/protocol.dart';
import '../services/session.dart';

/// Stream and recording status indicators with start/stop controls.
///
/// Reflects `obs.streaming` / `obs.recording` (protocol.md §4) and toggles them
/// via `streaming`/`recording` start·stop commands (§5). Rebuilds on state
/// changes so the indicators and the button labels track the server.
class StreamControlBar extends StatelessWidget {
  const StreamControlBar({super.key, required this.session});

  final Session session;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: session.state,
      builder: (context, _) {
        final state = session.state;
        final streaming = state.streaming ?? false;
        final recording = state.recording ?? false;
        final viewers = state.streamViewers;

        return Card(
          margin: const EdgeInsets.fromLTRB(16, 16, 16, 0),
          child: Padding(
            padding: const EdgeInsets.all(12),
            child: Wrap(
              spacing: 24,
              runSpacing: 12,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                _StatusControl(
                  title: 'Stream',
                  active: streaming,
                  activeLabel: 'LIVE',
                  inactiveLabel: 'Offline',
                  buttonLabel: streaming ? 'Stop' : 'Go Live',
                  icon: streaming ? Icons.stop : Icons.sensors,
                  onPressed: () => session.sendCommand(
                    Target.streaming,
                    streaming ? 'stop' : 'start',
                  ),
                ),
                _StatusControl(
                  title: 'Recording',
                  active: recording,
                  activeLabel: 'REC',
                  inactiveLabel: 'Idle',
                  buttonLabel: recording ? 'Stop' : 'Record',
                  icon: recording ? Icons.stop : Icons.fiber_manual_record,
                  onPressed: () => session.sendCommand(
                    Target.recording,
                    recording ? 'stop' : 'start',
                  ),
                ),
                if (viewers != null)
                  Text(
                    '$viewers ${viewers == 1 ? 'viewer' : 'viewers'}',
                    style: Theme.of(context).textTheme.bodyMedium,
                  ),
              ],
            ),
          ),
        );
      },
    );
  }
}

class _StatusControl extends StatelessWidget {
  const _StatusControl({
    required this.title,
    required this.active,
    required this.activeLabel,
    required this.inactiveLabel,
    required this.buttonLabel,
    required this.icon,
    required this.onPressed,
  });

  final String title;
  final bool active;
  final String activeLabel;
  final String inactiveLabel;
  final String buttonLabel;
  final IconData icon;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(title, style: Theme.of(context).textTheme.labelMedium),
            Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(
                  active ? Icons.circle : Icons.circle_outlined,
                  size: 12,
                  color: active ? scheme.error : scheme.outline,
                ),
                const SizedBox(width: 6),
                Text(
                  active ? activeLabel : inactiveLabel,
                  style: TextStyle(
                    fontWeight: active ? FontWeight.bold : FontWeight.normal,
                    color: active ? scheme.error : null,
                  ),
                ),
              ],
            ),
          ],
        ),
        const SizedBox(width: 12),
        active
            ? FilledButton.icon(
                onPressed: onPressed,
                icon: Icon(icon),
                label: Text(buttonLabel),
              )
            : OutlinedButton.icon(
                onPressed: onPressed,
                icon: Icon(icon),
                label: Text(buttonLabel),
              ),
      ],
    );
  }
}
