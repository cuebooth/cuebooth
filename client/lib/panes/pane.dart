import 'package:flutter/widgets.dart';

/// The edge a pane belongs to: where it docks, where its tab sits when
/// unpinned, and the direction it travels when summoned.
enum PaneEdge { top, bottom, left, right }

/// Something a pane needs the operator to see while the pane is not on screen.
///
/// An unpinned pane renders nothing, so a state its own body would have shown —
/// chat waiting to be authorized, say — reaches the operator only through its
/// tab.
@immutable
class PaneAttention {
  const PaneAttention({required this.source, required this.wanted});

  /// Notifies when [wanted] may have changed.
  final Listenable source;

  final bool Function() wanted;
}

/// One contributor to the layout.
///
/// Features register a pane rather than pushing a route, which is what lets the
/// operator see several at once (design.md §3.5 *Layout*).
@immutable
class PaneSpec {
  const PaneSpec({
    required this.id,
    required this.title,
    required this.icon,
    required this.builder,
    this.edge = PaneEdge.right,
    this.fillsCentre = false,
    this.startsPinned = true,
    this.attention,
  });

  /// Stable across releases — it keys the persisted layout, so changing it
  /// silently discards the operator's arrangement for that pane.
  final String id;

  final String title;
  final IconData icon;
  final WidgetBuilder builder;

  /// Where it sits when pinned to an edge, and where it enters from when not.
  final PaneEdge edge;

  /// When pinned, takes the centre rather than a strip on [edge].
  ///
  /// The centre is what the edges leave over, so at most one pane fills it; a
  /// second one pinned to the centre would have nowhere to go.
  final bool fillsCentre;

  /// Whether the pane is pinned the first time an operator ever sees it, before
  /// any saved layout exists.
  final bool startsPinned;

  /// Marks the pane's tab when it has something the operator should see.
  final PaneAttention? attention;
}

/// Which way a pane travels when it is summoned from [edge].
Offset paneEntryOffset(PaneEdge edge) => switch (edge) {
  PaneEdge.top => const Offset(0, -1),
  PaneEdge.bottom => const Offset(0, 1),
  PaneEdge.left => const Offset(-1, 0),
  PaneEdge.right => const Offset(1, 0),
};

/// Whether [edge] divides the window horizontally, so a pinned pane there is
/// sized by width rather than height.
bool edgeIsHorizontal(PaneEdge edge) =>
    edge == PaneEdge.left || edge == PaneEdge.right;
