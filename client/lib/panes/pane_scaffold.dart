import 'dart:math' as math;

import 'package:flutter/foundation.dart' show setEquals;
import 'package:flutter/material.dart';

import 'pane.dart';
import 'pane_layout.dart';

/// Thickness of an edge's tab strip. The dock is inset by this on every edge
/// that has one, so a tab never sits over the pane behind it.
const double tabStripThickness = 36;

/// Space a divider occupies between the centre and a pinned pane.
///
/// The line drawn in it is 1px; the rest is grab room. What sits next to a
/// divider is the button grid, which fires Companion on tap-down, so a grab
/// that lands beside the divider presses a cue rather than missing harmlessly.
/// Still under the 44pt/48dp both platforms ask for — that would cost a gutter
/// wide enough to notice between every pane.
const double dividerThickness = 24;

/// The extent a pane is given along its own axis wherever the axis allows it.
///
/// A pane narrower than its header cannot lay the header out, and the pin — the
/// only control that would restore it — is pushed outside the window. Where the
/// axis is too small to give every pane this much, they share what there is
/// instead: see [allocatePaneExtents].
const double minPaneExtent = 120;

/// Least the centre keeps whatever the edges ask for.
const double minCentreExtent = 160;

/// How long a summoned pane takes to travel in or out.
const Duration paneTransition = Duration(milliseconds: 180);

/// Extents for the pinned panes along one axis, in the order given.
///
/// A stored fraction is a request rather than the last word: it is a fraction of
/// the window with no absolute floor, so on a narrow window it resolves to an
/// extent too small for the pane to be usable, and several pinned panes can
/// between them ask for more than the axis holds.
///
/// Each request is raised to [minPaneExtent] and the centre keeps
/// [minCentreExtent]. When even that does not fit, every pane is scaled down
/// together — below the floor, because an axis that small has nothing better to
/// offer, and starving the panes beats overflowing the dock. A pane starved
/// that far loses its header and with it its pin, and a pinned pane has no tab,
/// so on an axis under roughly 230px it is the window that has to give:
/// widening it restores the pane and the size it was left at.
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
  return [for (final extent in extents) extent * room / total];
}

/// The extent a summoned pane covers along its own axis.
///
/// Floating panes are not bound by the centre's floor — covering the dock is
/// what they are for — but they carry the same lower bound, since a pane too
/// narrow for its own header cannot be dismissed from its pin either.
double floatingPaneExtent(double fraction, double available) {
  if (available <= 0) return 0;
  return math.min(math.max(available * fraction, minPaneExtent), available);
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
class PaneScaffold extends StatefulWidget {
  const PaneScaffold({super.key, required this.layout, this.emptyCentre});

  final PaneLayout layout;

  /// Shown when no pane is pinned to the centre. Every pane can be unpinned, so
  /// this is a state the operator can always reach.
  final Widget? emptyCentre;

  @override
  State<PaneScaffold> createState() => _PaneScaffoldState();
}

class _PaneScaffoldState extends State<PaneScaffold> {
  /// Panes drawn over the dock. A dismissed pane stays here until it has
  /// travelled back to its edge, which is the only way it can be seen leaving.
  final Set<String> _drawn = {};

  @override
  void initState() {
    super.initState();
    widget.layout.addListener(_syncDrawn);
    _syncDrawn();
  }

  @override
  void didUpdateWidget(PaneScaffold old) {
    super.didUpdateWidget(old);
    if (old.layout != widget.layout) {
      old.layout.removeListener(_syncDrawn);
      widget.layout.addListener(_syncDrawn);
      _drawn.clear();
      _syncDrawn();
    }
  }

  @override
  void dispose() {
    widget.layout.removeListener(_syncDrawn);
    super.dispose();
  }

  void _syncDrawn() {
    final layout = widget.layout;
    final summoned = {for (final pane in layout.floating) pane.id};
    final next = {..._drawn, ...summoned};
    // A pane that has been pinned or withdrawn is not leaving the screen — it
    // is moving into the dock, or gone entirely. Travelling it out would draw
    // it twice on its way to a place it already is.
    next.removeWhere(
      (id) =>
          !summoned.contains(id) &&
          (layout.isPinned(id) || !layout.isEnabled(id)),
    );
    if (setEquals(next, _drawn)) return;
    setState(() {
      _drawn
        ..clear()
        ..addAll(next);
    });
  }

  void _retired(String id) {
    if (!mounted || !_drawn.contains(id)) return;
    setState(() => _drawn.remove(id));
  }

  @override
  Widget build(BuildContext context) {
    final layout = widget.layout;
    return ListenableBuilder(
      listenable: layout,
      builder: (context, _) {
        final tabbed = {
          for (final edge in PaneEdge.values) edge: layout.tabsAt(edge),
        };
        final insets = EdgeInsets.only(
          left: tabbed[PaneEdge.left]!.isEmpty ? 0 : tabStripThickness,
          right: tabbed[PaneEdge.right]!.isEmpty ? 0 : tabStripThickness,
          top: tabbed[PaneEdge.top]!.isEmpty ? 0 : tabStripThickness,
          bottom: tabbed[PaneEdge.bottom]!.isEmpty ? 0 : tabStripThickness,
        );

        final summoned = {for (final pane in layout.floating) pane.id};
        return LayoutBuilder(
          builder: (context, constraints) => Stack(
            // Every child here is positioned. A stack with one that is not
            // sizes itself to that child instead of to its constraints, and the
            // whole dock collapses to it.
            children: [
              Positioned.fill(
                child: Padding(padding: insets, child: _buildDock(context)),
              ),
              for (final edge in PaneEdge.values)
                if (tabbed[edge]!.isNotEmpty)
                  _tabStrip(context, edge, tabbed[edge]!, insets),
              for (final id in _drawn)
                if (layout.spec(id) case final pane?)
                  _floatingPane(
                    context,
                    pane,
                    insets,
                    constraints.biggest,
                    visible: summoned.contains(id),
                  ),
            ],
          ),
        );
      },
    );
  }

  Widget _buildDock(BuildContext context) {
    final layout = widget.layout;
    return LayoutBuilder(
      builder: (context, constraints) {
        final centre = layout.centrePane;
        Widget middle = centre == null
            ? (widget.emptyCentre ?? const SizedBox.expand())
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
          // An axis with no room gives every pane a zero extent; its divider
          // has no pane to move and nothing to divide, and laying one out is
          // what pushes the row past the axis it is already too big for.
          if (heights[i] == 0) continue;
          column.add(
            SizedBox(
              height: heights[i],
              child: _PaneFrame(layout: layout, pane: top[i]),
            ),
          );
          column.add(
            _divider(
              context,
              top[i],
              constraints.maxHeight,
              axisPanes: vertical,
              axisExtents: heights,
              index: i,
            ),
          );
        }
        column.add(Expanded(child: middle));
        for (var i = 0; i < bottom.length; i++) {
          if (heights[top.length + i] == 0) continue;
          column.add(
            _divider(
              context,
              bottom[i],
              constraints.maxHeight,
              axisPanes: vertical,
              axisExtents: heights,
              index: top.length + i,
              before: true,
            ),
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
          if (widths[i] == 0) continue;
          row.add(
            SizedBox(
              width: widths[i],
              child: _PaneFrame(layout: layout, pane: left[i]),
            ),
          );
          row.add(
            _divider(
              context,
              left[i],
              constraints.maxWidth,
              axisPanes: horizontal,
              axisExtents: widths,
              index: i,
            ),
          );
        }
        row.add(Expanded(child: middle));
        for (var i = 0; i < right.length; i++) {
          if (widths[left.length + i] == 0) continue;
          row.add(
            _divider(
              context,
              right[i],
              constraints.maxWidth,
              axisPanes: horizontal,
              axisExtents: widths,
              index: left.length + i,
              before: true,
            ),
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
    required List<PaneSpec> axisPanes,
    required List<double> axisExtents,
    required int index,
    bool before = false,
  }) {
    final layout = widget.layout;
    final horizontal = edgeIsHorizontal(pane.edge);
    final sign = before ? -1 : 1;

    // Both bounds stop where the dock stops honouring the fraction. Tracking
    // only the render would leave the stored value drifting past the clamp,
    // which the operator feels as travel that moves nothing on the way back —
    // so the ceiling has to account for the other panes sharing this axis, not
    // just this one.
    void drag(double delta) {
      if (available <= 0) return;
      final low = (minPaneExtent / available).clamp(
        minPaneFraction,
        maxPaneFraction,
      );
      // Measured against what the other panes are rendered at, not what they
      // asked for. The two part company exactly when the axis is
      // over-subscribed and the dock is scaling everyone down — and a bound
      // taken from the requests then sits far from what is on screen, so the
      // first pixel of drag snaps the layout to it.
      var others = 0.0;
      for (var i = 0; i < axisExtents.length; i++) {
        if (i != index) others += axisExtents[i];
      }
      final room =
          available - axisExtents.length * dividerThickness - minCentreExtent;
      final ceiling = math.max(minPaneExtent, room - others);
      final high = (ceiling / available).clamp(
        minPaneFraction,
        maxPaneFraction,
      );
      final wanted = layout.fractionOf(pane.id) + sign * delta / available;
      layout.setFraction(pane.id, wanted.clamp(low, high));
    }

    // A window that shrank leaves every stored fraction on the axis larger
    // than what the dock renders, and a drag against those stale numbers moves
    // panes the operator is not touching. Grabbing a divider is a deliberate
    // resize of this axis, so it is the moment to adopt what is on screen —
    // whereas a rotation, which is not a resize gesture, leaves the operator's
    // proportions alone to come back to.
    void adoptRenderedExtents() {
      if (available <= 0) return;
      for (var i = 0; i < axisPanes.length; i++) {
        layout.setFraction(axisPanes[i].id, axisExtents[i] / available);
      }
    }

    return MouseRegion(
      key: paneDividerKey(pane.id),
      cursor: horizontal
          ? SystemMouseCursors.resizeLeftRight
          : SystemMouseCursors.resizeUpDown,
      child: GestureDetector(
        behavior: HitTestBehavior.opaque,
        onHorizontalDragStart: horizontal
            ? (_) => adoptRenderedExtents()
            : null,
        onVerticalDragStart: horizontal ? null : (_) => adoptRenderedExtents(),
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

  Widget _tabStrip(
    BuildContext context,
    PaneEdge edge,
    List<PaneSpec> tabs,
    EdgeInsets insets,
  ) {
    final horizontal = edgeIsHorizontal(edge);
    final children = [
      for (final pane in tabs)
        _PaneTab(layout: widget.layout, pane: pane, edge: edge),
    ];

    return Positioned(
      // The stack's children vary in number and order with what is pinned.
      // Unkeyed, Flutter matches them positionally, and a strip appearing
      // re-associates a floating pane's element with a different pane —
      // restarting its travel and rebuilding its body from scratch.
      key: ValueKey('pane-strip-${edge.name}'),
      left: edge == PaneEdge.right ? null : 0,
      right: edge == PaneEdge.left ? null : 0,
      // A full-height side strip would cover the ends of a top or bottom strip
      // and take their taps, so the side strips yield the corners.
      top: edge == PaneEdge.bottom ? null : (horizontal ? insets.top : 0),
      bottom: edge == PaneEdge.top ? null : (horizontal ? insets.bottom : 0),
      width: horizontal ? tabStripThickness : null,
      height: horizontal ? null : tabStripThickness,
      child: Material(
        color: Theme.of(context).colorScheme.surfaceContainerHighest,
        child: SingleChildScrollView(
          scrollDirection: horizontal ? Axis.vertical : Axis.horizontal,
          child: horizontal
              ? Column(mainAxisSize: MainAxisSize.min, children: children)
              : Row(mainAxisSize: MainAxisSize.min, children: children),
        ),
      ),
    );
  }

  /// A summoned pane, sized against the dock area rather than the window, so it
  /// covers the same extent it would occupy pinned.
  Widget _floatingPane(
    BuildContext context,
    PaneSpec pane,
    EdgeInsets insets,
    Size window, {
    required bool visible,
  }) {
    final horizontal = edgeIsHorizontal(pane.edge);
    final available = horizontal
        ? window.width - insets.horizontal
        : window.height - insets.vertical;
    // A centre pane has no divider, so its fraction is whatever the default
    // was and nothing can change it. Summoned as a strip of that width it
    // would be a sliver of the thing it is — a button grid squeezed to 30% —
    // so it comes back over the dock at the size it held in it.
    final extent = pane.fillsCentre
        ? available
        : floatingPaneExtent(widget.layout.fractionOf(pane.id), available);

    return Positioned(
      key: ValueKey('pane-float-${pane.id}'),
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
        visible: visible,
        onRetired: () => _retired(pane.id),
        child: Material(
          elevation: 8,
          child: _PaneFrame(layout: widget.layout, pane: pane),
        ),
      ),
    );
  }
}

/// Travels a pane in from its edge when summoned, and back out when dismissed.
class _SlideIn extends StatefulWidget {
  const _SlideIn({
    required this.edge,
    required this.visible,
    required this.onRetired,
    required this.child,
  });

  final PaneEdge edge;
  final bool visible;

  /// Called once the pane has finished travelling out, so it can stop being
  /// drawn.
  final VoidCallback onRetired;

  final Widget child;

  @override
  State<_SlideIn> createState() => _SlideInState();
}

class _SlideInState extends State<_SlideIn>
    with SingleTickerProviderStateMixin {
  late final AnimationController _controller = AnimationController(
    vsync: this,
    duration: paneTransition,
  );

  @override
  void initState() {
    super.initState();
    _controller.addStatusListener(_onStatus);
    if (widget.visible) {
      _controller.forward();
    } else {
      // Summoned and dismissed inside one frame: the controller is already at
      // rest, so no status will ever fire and the pane would stay mounted and
      // off-screen for the life of the dock.
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted && !widget.visible) widget.onRetired();
      });
    }
  }

  @override
  void didUpdateWidget(_SlideIn old) {
    super.didUpdateWidget(old);
    if (widget.visible != old.visible) {
      widget.visible ? _controller.forward() : _controller.reverse();
    }
  }

  void _onStatus(AnimationStatus status) {
    if (status == AnimationStatus.dismissed && !widget.visible) {
      widget.onRetired();
    }
  }

  @override
  void dispose() {
    _controller.removeStatusListener(_onStatus);
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return SlideTransition(
      position:
          Tween<Offset>(
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
    return LayoutBuilder(
      builder: (context, frame) => Column(
        children: [
          // A pane shorter than its own header has no room for one: the header is
          // a fixed height, and a Column that cannot fit its children reports an
          // overflow rather than trimming them.
          if (frame.maxHeight >= 44)
            Container(
              height: 32,
              color: Theme.of(context).colorScheme.surfaceContainerHigh,
              // Below ~44px even the pin alone does not fit. An axis that narrow
              // has nothing usable to offer either way, so the header is clipped
              // rather than allowed to paint outside the pane it belongs to.
              child: ClipRect(
                child: LayoutBuilder(
                  builder: (context, constraints) {
                    // The pin is the only control that can restore a pane the
                    // operator has shrunk, so it is the last thing to go: the icon
                    // and then the title yield to it rather than overflowing.
                    final width = constraints.maxWidth;
                    // Narrower than the pin itself. An axis this small cannot show
                    // a header at all, and a Row that does not fit reports an
                    // overflow rather than shrinking to suit.
                    if (width < 44) return const SizedBox.shrink();
                    return Row(
                      children: [
                        if (width >= 112) ...[
                          const SizedBox(width: 8),
                          _withAttention(pane, Icon(pane.icon, size: 16)),
                        ],
                        const SizedBox(width: 8),
                        Expanded(
                          child: width >= 80
                              ? Text(
                                  pane.title,
                                  style: Theme.of(context).textTheme.labelLarge,
                                  overflow: TextOverflow.ellipsis,
                                  softWrap: false,
                                )
                              : const SizedBox.shrink(),
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
                          tooltip: pinned
                              ? 'Unpin ${pane.title}'
                              : 'Pin ${pane.title}',
                          icon: Icon(
                            pinned ? Icons.push_pin : Icons.push_pin_outlined,
                          ),
                          onPressed: () => layout.togglePin(pane.id),
                        ),
                        const SizedBox(width: 4),
                      ],
                    );
                  },
                ),
              ),
            ),
          Expanded(child: pane.builder(context)),
        ],
      ),
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

    return Material(
      color: summoned
          ? Theme.of(context).colorScheme.secondaryContainer
          : Colors.transparent,
      child: InkWell(
        onTap: () => layout.toggleSummoned(pane.id),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
          child: edgeIsHorizontal(edge)
              // A vertical strip has no width for a horizontal label, so it
              // turns to read up the left edge and down the right.
              ? RotatedBox(
                  quarterTurns: edge == PaneEdge.left ? 3 : 1,
                  child: label,
                )
              : label,
        ),
      ),
    );
  }
}
