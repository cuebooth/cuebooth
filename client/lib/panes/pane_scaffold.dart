import 'dart:math' as math;

import 'package:flutter/material.dart';

import 'pane.dart';
import 'pane_layout.dart';

/// Thickness of an edge's tab strip. The dock is inset by this on every edge
/// that has one, so a tab never sits over the pane behind it.
const double tabStripThickness = 36;

/// Grab width of a divider between the centre and a pinned pane.
const double dividerThickness = 8;

/// Least a pinned pane may occupy along its own axis.
///
/// A pane narrower than its header cannot lay the header out, and the pin — the
/// only control that would restore it — is pushed outside the window, so the
/// operator can no longer unpin what they have just shrunk.
const double minPaneExtent = 120;

/// Least the centre keeps whatever the edges ask for.
const double minCentreExtent = 160;

/// Extents for the pinned panes along one axis, in the order given.
///
/// A stored fraction is a request rather than the last word: it is a fraction of
/// the window with no absolute floor, so on a narrow window it resolves to an
/// extent too small for the pane to be usable, and several pinned panes can
/// between them ask for more than the axis holds.
List<double> allocatePaneExtents({
  required List<double> fractions,
  required double available,
  required int dividerCount,
}) {
  if (fractions.isEmpty) return const [];
  final room = available - dividerCount * dividerThickness - minCentreExtent;
  if (room <= 0) return List<double>.filled(fractions.length, 0);

  final extents = [
    for (final fraction in fractions)
      math.max(available * fraction, minPaneExtent),
  ];
  final total = extents.fold<double>(0, (sum, e) => sum + e);
  if (total <= room) return extents;
  // Not enough for every floor: share what there is, so a window dragged narrow
  // starves the panes rather than overflowing the dock.
  return [for (final extent in extents) extent * room / total];
}

/// Identifies a pane's rendered frame.
Key paneKey(String id) => ValueKey('pane-$id');

/// Identifies the draggable boundary belonging to a pane.
Key paneDividerKey(String id) => ValueKey('pane-divider-$id');

/// Identifies the tab that summons an unpinned pane.
Key paneTabKey(String id) => ValueKey('pane-tab-$id');

/// Renders the operator's arrangement: pinned panes holding the layout,
/// unpinned ones waiting behind a tab, and whatever is summoned floating over
/// the top (design.md §3.5 *Layout*).
class PaneScaffold extends StatelessWidget {
  const PaneScaffold({super.key, required this.layout, this.emptyCentre});

  final PaneLayout layout;

  /// Shown when no pane is pinned to the centre. Every pane can be unpinned, so
  /// this is a state the operator can always reach.
  final Widget? emptyCentre;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: layout,
      builder: (context, _) {
        final insets = EdgeInsets.only(
          left: layout.tabsAt(PaneEdge.left).isEmpty ? 0 : tabStripThickness,
          right: layout.tabsAt(PaneEdge.right).isEmpty ? 0 : tabStripThickness,
          top: layout.tabsAt(PaneEdge.top).isEmpty ? 0 : tabStripThickness,
          bottom: layout.tabsAt(PaneEdge.bottom).isEmpty
              ? 0
              : tabStripThickness,
        );

        return LayoutBuilder(
          builder: (context, constraints) => Stack(
            // Positioned children do not size a stack, so the dock's extent
            // comes from the incoming constraints rather than from whatever
            // happens to be in it.
            fit: StackFit.expand,
            children: [
              Positioned.fill(
                child: Padding(padding: insets, child: _buildDock(context)),
              ),
              for (final edge in PaneEdge.values)
                if (layout.tabsAt(edge).isNotEmpty) _tabStrip(context, edge),
              for (final pane in layout.floating)
                _floatingPane(context, pane, insets, constraints.biggest),
            ],
          ),
        );
      },
    );
  }

  Widget _buildDock(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        final centre = layout.centrePane;
        Widget middle = centre == null
            ? (emptyCentre ?? const SizedBox.expand())
            : _PaneFrame(layout: layout, pane: centre);

        // Top and bottom sit between the left and right panes, so they are
        // nested inside the column that the row leaves for the centre.
        final top = layout.pinnedAt(PaneEdge.top);
        final bottom = layout.pinnedAt(PaneEdge.bottom);
        final vertical = [...top, ...bottom];
        final heights = allocatePaneExtents(
          fractions: [for (final p in vertical) layout.fractionOf(p.id)],
          available: constraints.maxHeight,
          dividerCount: vertical.length,
        );

        final column = <Widget>[];
        for (var i = 0; i < top.length; i++) {
          column.add(
            SizedBox(
              height: heights[i],
              child: _PaneFrame(layout: layout, pane: top[i]),
            ),
          );
          column.add(_divider(context, top[i], constraints.maxHeight));
        }
        column.add(Expanded(child: middle));
        for (var i = 0; i < bottom.length; i++) {
          column.add(
            _divider(context, bottom[i], constraints.maxHeight, before: true),
          );
          column.add(
            SizedBox(
              height: heights[top.length + i],
              child: _PaneFrame(layout: layout, pane: bottom[i]),
            ),
          );
        }
        middle = Column(children: column);

        final left = layout.pinnedAt(PaneEdge.left);
        final right = layout.pinnedAt(PaneEdge.right);
        final horizontal = [...left, ...right];
        final widths = allocatePaneExtents(
          fractions: [for (final p in horizontal) layout.fractionOf(p.id)],
          available: constraints.maxWidth,
          dividerCount: horizontal.length,
        );

        final row = <Widget>[];
        for (var i = 0; i < left.length; i++) {
          row.add(
            SizedBox(
              width: widths[i],
              child: _PaneFrame(layout: layout, pane: left[i]),
            ),
          );
          row.add(_divider(context, left[i], constraints.maxWidth));
        }
        row.add(Expanded(child: middle));
        for (var i = 0; i < right.length; i++) {
          row.add(
            _divider(context, right[i], constraints.maxWidth, before: true),
          );
          row.add(
            SizedBox(
              width: widths[left.length + i],
              child: _PaneFrame(layout: layout, pane: right[i]),
            ),
          );
        }
        return Row(children: row);
      },
    );
  }

  /// A draggable boundary. [before] flips the drag sign for panes on the far
  /// side, where dragging towards the centre grows rather than shrinks them.
  Widget _divider(
    BuildContext context,
    PaneSpec pane,
    double available, {
    bool before = false,
  }) {
    final horizontal = edgeIsHorizontal(pane.edge);
    final sign = before ? -1 : 1;

    // Stop at the extent the pane needs to stay usable rather than at the bare
    // fraction, or the drag keeps shrinking a value the dock has already
    // stopped honouring and the pane jumps when the window next changes size.
    void drag(double delta) {
      final floor = available <= 0
          ? minPaneFraction
          : (minPaneExtent / available).clamp(minPaneFraction, maxPaneFraction);
      layout.setFraction(
        pane.id,
        (layout.fractionOf(pane.id) + sign * delta / available).clamp(
          floor,
          maxPaneFraction,
        ),
      );
    }

    return MouseRegion(
      key: paneDividerKey(pane.id),
      cursor: horizontal
          ? SystemMouseCursors.resizeLeftRight
          : SystemMouseCursors.resizeUpDown,
      child: GestureDetector(
        behavior: HitTestBehavior.opaque,
        onHorizontalDragUpdate: horizontal
            ? (details) => drag(details.delta.dx)
            : null,
        onVerticalDragUpdate: horizontal
            ? null
            : (details) => drag(details.delta.dy),
        child: SizedBox(
          width: horizontal ? dividerThickness : null,
          height: horizontal ? null : dividerThickness,
          child: Center(
            child: Container(
              width: horizontal ? 1 : null,
              height: horizontal ? null : 1,
              color: Theme.of(context).dividerColor,
            ),
          ),
        ),
      ),
    );
  }

  Widget _tabStrip(BuildContext context, PaneEdge edge) {
    final tabs = layout.tabsAt(edge);
    final horizontal = edgeIsHorizontal(edge);
    final children = [
      for (final pane in tabs) _PaneTab(layout: layout, pane: pane, edge: edge),
    ];

    return Positioned(
      left: edge == PaneEdge.right ? null : 0,
      right: edge == PaneEdge.left ? null : 0,
      top: edge == PaneEdge.bottom ? null : 0,
      bottom: edge == PaneEdge.top ? null : 0,
      width: horizontal ? tabStripThickness : null,
      height: horizontal ? null : tabStripThickness,
      child: ColoredBox(
        color: Theme.of(context).colorScheme.surfaceContainerHighest,
        child: horizontal
            ? Column(children: children)
            : Row(children: children),
      ),
    );
  }

  /// A summoned pane, sized against the dock area rather than the window, so it
  /// covers the same extent it would occupy pinned.
  Widget _floatingPane(
    BuildContext context,
    PaneSpec pane,
    EdgeInsets insets,
    Size window,
  ) {
    final horizontal = edgeIsHorizontal(pane.edge);
    final fraction = layout.fractionOf(pane.id);
    final extent = horizontal
        ? (window.width - insets.horizontal) * fraction
        : (window.height - insets.vertical) * fraction;

    return Positioned(
      // The axis it flies along is pinned to its own edge and given an explicit
      // extent; the cross axis spans the dock.
      left: pane.edge == PaneEdge.right ? null : insets.left,
      right: pane.edge == PaneEdge.left ? null : insets.right,
      top: pane.edge == PaneEdge.bottom ? null : insets.top,
      bottom: pane.edge == PaneEdge.top ? null : insets.bottom,
      width: horizontal ? extent : null,
      height: horizontal ? null : extent,
      child: _SlideIn(
        edge: pane.edge,
        child: Material(
          elevation: 8,
          child: _PaneFrame(layout: layout, pane: pane),
        ),
      ),
    );
  }
}

/// Animates a summoned pane in from its edge, and out again when dismissed.
class _SlideIn extends StatefulWidget {
  const _SlideIn({required this.edge, required this.child});

  final PaneEdge edge;
  final Widget child;

  @override
  State<_SlideIn> createState() => _SlideInState();
}

class _SlideInState extends State<_SlideIn>
    with SingleTickerProviderStateMixin {
  late final AnimationController _controller = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 180),
  )..forward();

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return SlideTransition(
      position: Tween<Offset>(
        begin: paneEntryOffset(widget.edge),
        end: Offset.zero,
      ).animate(
        CurvedAnimation(parent: _controller, curve: Curves.easeOutCubic),
      ),
      child: widget.child,
    );
  }
}

/// A pane's chrome: its title, and the pin that moves it between the layout and
/// its tab.
class _PaneFrame extends StatelessWidget {
  _PaneFrame({required this.layout, required this.pane})
    : super(key: paneKey(pane.id));

  final PaneLayout layout;
  final PaneSpec pane;

  @override
  Widget build(BuildContext context) {
    final pinned = layout.isPinned(pane.id);
    return Column(
      children: [
        Container(
          height: 32,
          color: Theme.of(context).colorScheme.surfaceContainerHigh,
          child: Row(
            children: [
              const SizedBox(width: 8),
              Icon(pane.icon, size: 16),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  pane.title,
                  style: Theme.of(context).textTheme.labelLarge,
                  overflow: TextOverflow.ellipsis,
                ),
              ),
              IconButton(
                iconSize: 16,
                // A default icon button reserves a 48px tap target from the
                // theme regardless of its box, which this bar would clip —
                // taking the hit area with it.
                style: IconButton.styleFrom(
                  tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                ),
                padding: EdgeInsets.zero,
                constraints: const BoxConstraints.tightFor(
                  width: 32,
                  height: 32,
                ),
                tooltip: pinned ? 'Unpin ${pane.title}' : 'Pin ${pane.title}',
                icon: Icon(pinned ? Icons.push_pin : Icons.push_pin_outlined),
                onPressed: () => layout.togglePin(pane.id),
              ),
              const SizedBox(width: 4),
            ],
          ),
        ),
        Expanded(child: pane.builder(context)),
      ],
    );
  }
}

/// Marks [child] while the pane has something the operator should see. An
/// unpinned pane renders nothing, so its tab is the only place such a state can
/// reach them.
Widget _withAttention(PaneSpec pane, Widget child) {
  final attention = pane.attention;
  if (attention == null) return child;
  return ListenableBuilder(
    listenable: attention.source,
    builder: (context, _) => attention.wanted()
        // A dot rather than a count: there is one thing to do, which is look.
        ? Badge(smallSize: 6, child: child)
        : child,
  );
}

/// The control that summons an unpinned pane from its edge.
class _PaneTab extends StatelessWidget {
  _PaneTab({required this.layout, required this.pane, required this.edge})
    : super(key: paneTabKey(pane.id));

  final PaneLayout layout;
  final PaneSpec pane;
  final PaneEdge edge;

  @override
  Widget build(BuildContext context) {
    final summoned = layout.isVisible(pane.id);
    final label = Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        _withAttention(pane, Icon(pane.icon, size: 14)),
        const SizedBox(width: 6),
        Text(pane.title, style: Theme.of(context).textTheme.labelMedium),
      ],
    );

    return InkWell(
      onTap: () => layout.toggleSummoned(pane.id),
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
        color: summoned
            ? Theme.of(context).colorScheme.secondaryContainer
            : null,
        child: edgeIsHorizontal(edge)
            // A vertical strip has no width for a horizontal label, so it turns
            // to read up the left edge and down the right.
            ? RotatedBox(
                quarterTurns: edge == PaneEdge.left ? 3 : 1,
                child: label,
              )
            : label,
      ),
    );
  }
}
