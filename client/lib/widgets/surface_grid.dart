import 'package:flutter/material.dart';

import '../services/session.dart';
import '../services/surface_state.dart';

/// The operator's control surface: the Companion Satellite button grid, rendered
/// natively (protocol.md §10).
///
/// Every button — its label, icon, color, and live feedback — is rendered by
/// Companion and streamed to the client, so the grid is exactly whatever
/// Companion is configured with, with nothing defined or maintained client-side.
/// Taps are sent back to Companion as key presses.
class SurfaceGrid extends StatelessWidget {
  const SurfaceGrid({super.key, required this.session});

  final Session session;

  @override
  Widget build(BuildContext context) {
    final surface = session.surface;
    return ListenableBuilder(
      listenable: surface,
      builder: (context, _) {
        if (!surface.hasLayout) {
          return const Center(
            child: Padding(
              padding: EdgeInsets.all(24),
              child: Text(
                'Waiting for the Companion surface…',
                style: TextStyle(fontSize: 16),
              ),
            ),
          );
        }
        final cols = surface.cols;
        final count = surface.rows * cols;
        return Padding(
          padding: const EdgeInsets.all(8),
          child: GridView.builder(
            gridDelegate: SliverGridDelegateWithFixedCrossAxisCount(
              crossAxisCount: cols,
              mainAxisSpacing: 6,
              crossAxisSpacing: 6,
            ),
            itemCount: count,
            itemBuilder: (context, index) => _SurfaceCell(
              keyState: surface.keyAt(index),
              // The grid index is the flat key index (row * cols + col).
              onPress: (down) => session.sendSurfacePress(index, down),
            ),
          ),
        );
      },
    );
  }
}

/// One button cell. Tracks a local pressed state for immediate visual feedback
/// (the authoritative feedback from Companion follows in a key update).
class _SurfaceCell extends StatefulWidget {
  const _SurfaceCell({required this.keyState, required this.onPress});

  final SurfaceKey? keyState;
  final void Function(bool down) onPress;

  @override
  State<_SurfaceCell> createState() => _SurfaceCellState();
}

class _SurfaceCellState extends State<_SurfaceCell> {
  bool _down = false;

  void _setDown(bool down) {
    // Only emit on an actual transition: gesture callbacks can repeat a state
    // (e.g. onTapUp then onTapCancel both release), and a duplicate frame would
    // be needless surface-press traffic to the server/Companion.
    if (_down == down) return;
    setState(() => _down = down);
    widget.onPress(down);
  }

  @override
  void dispose() {
    // If the cell is torn down while held (e.g. a surface re-baseline rebuilds
    // the grid), release so Companion isn't left holding the button down.
    if (_down) widget.onPress(false);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final ks = widget.keyState;
    // An empty cell (no button configured at this position) is a dark, inert
    // placeholder so the grid keeps Companion's layout.
    if (ks == null) {
      return DecoratedBox(
        decoration: BoxDecoration(
          color: Colors.black26,
          borderRadius: BorderRadius.circular(6),
        ),
      );
    }

    final pressed = _down || ks.pressed;
    final radius = BorderRadius.circular(6);
    // The pointer handlers use down/up (not onTap), so GestureDetector exposes
    // no activatable semantics on its own. Wrap a button node around it so
    // screen readers / switch control can discover and trigger the cell.
    // Companion bakes each button's label into the bitmap, so the best
    // accessible name we can give is its grid position; an assistive-tech
    // activation maps to a momentary press (down then up), matching Companion's
    // typical trigger buttons.
    return Semantics(
      button: true,
      label: 'Button row ${ks.row + 1}, column ${ks.col + 1}',
      onTap: () {
        _setDown(true);
        _setDown(false);
      },
      child: GestureDetector(
        onTapDown: (_) => _setDown(true),
        onTapUp: (_) => _setDown(false),
        onTapCancel: () => _setDown(false),
        child: AnimatedScale(
          scale: pressed ? 0.92 : 1.0,
          duration: const Duration(milliseconds: 60),
          child: DecoratedBox(
            decoration: BoxDecoration(
              color: ks.color != null ? Color(ks.color!) : Colors.black,
              borderRadius: radius,
              border: pressed
                  ? Border.all(color: Colors.white, width: 2)
                  : null,
            ),
            child: ClipRRect(
              borderRadius: radius,
              child: ks.image != null
                  ? RawImage(image: ks.image, fit: BoxFit.contain)
                  : _Fallback(keyType: ks.keyType),
            ),
          ),
        ),
      ),
    );
  }
}

/// Shown when a key has no bitmap yet (or bitmaps are disabled): a glyph for
/// page-navigation keys, otherwise nothing over the background color.
class _Fallback extends StatelessWidget {
  const _Fallback({required this.keyType});

  final String keyType;

  @override
  Widget build(BuildContext context) {
    final label = switch (keyType) {
      'PAGEUP' => '▲',
      'PAGEDOWN' => '▼',
      'PAGENUM' => '•',
      _ => '',
    };
    if (label.isEmpty) return const SizedBox.expand();
    return Center(
      child: Text(
        label,
        style: const TextStyle(color: Colors.white70, fontSize: 18),
      ),
    );
  }
}
