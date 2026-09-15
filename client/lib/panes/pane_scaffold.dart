import 'package:flutter/material.dart';

import 'pane.dart';
import 'pane_layout.dart';

/// Thickness of an edge's tab strip. The dock is inset by this on every edge
/// that has one, so a tab never sits over the pane behind it.
const double tabStripThickness = 36;

/// Grab width of a divider between the centre and a pinned pane.
const double dividerThickness = 8;

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
        final column = <Widget>[];
        for (final pane in layout.pinnedAt(PaneEdge.top)) {
          column.add(
            _sized(pane, constraints.maxHeight, _PaneFrame(layout: layout, pane: pane)),
          );
          column.add(_divider(context, pane, constraints.maxHeight));
        }
        column.add(Expanded(child: middle));
        for (final pane in layout.pinnedAt(PaneEdge.bottom)) {
          column.add(_divider(context, pane, constraints.maxHeight, before: true));
          column.add(
            _sized(pane, constraints.maxHeight, _PaneFrame(layout: layout, pane: pane)),
          );
        }
        middle = Column(children: column);

        final row = <Widget>[];
        for (final pane in layout.pinnedAt(PaneEdge.left)) {
          row.add(
            _sized(pane, constraints.maxWidth, _PaneFrame(layout: layout, pane: pane)),
          );
          row.add(_divider(context, pane, constraints.maxWidth));
        }
        row.add(Expanded(child: middle));
        for (final pane in layout.pinnedAt(PaneEdge.right)) {
          row.add(_divider(context, pane, constraints.maxWidth, before: true));
          row.add(
            _sized(pane, constraints.maxWidth, _PaneFrame(layout: layout, pane: pane)),
          );
        }
        return Row(children: row);
      },
    );
  }

  Widget _sized(PaneSpec pane, double available, Widget child) {
    final extent = available * layout.fractionOf(pane.id);
    return edgeIsHorizontal(pane.edge)
        ? SizedBox(width: extent, child: child)
        : SizedBox(height: extent, child: child);
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
    return MouseRegion(
      cursor: horizontal
          ? SystemMouseCursors.resizeLeftRight
          : SystemMouseCursors.resizeUpDown,
      child: GestureDetector(
        behavior: HitTestBehavior.opaque,
        onHorizontalDragUpdate: horizontal
            ? (details) => layout.setFraction(
                pane.id,
                layout.fractionOf(pane.id) + sign * details.delta.dx / available,
              )
            : null,
        onVerticalDragUpdate: horizontal
            ? null
            : (details) => layout.setFraction(
                pane.id,
                layout.fractionOf(pane.id) + sign * details.delta.dy / available,
              ),
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
  const _PaneFrame({required this.layout, required this.pane});

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
                // A default icon button reserves a 48px target, which this bar
                // would clip — taking the hit area with it.
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

/// The control that summons an unpinned pane from its edge.
class _PaneTab extends StatelessWidget {
  const _PaneTab({
    required this.layout,
    required this.pane,
    required this.edge,
  });

  final PaneLayout layout;
  final PaneSpec pane;
  final PaneEdge edge;

  @override
  Widget build(BuildContext context) {
    final summoned = layout.isVisible(pane.id);
    final label = Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Icon(pane.icon, size: 14),
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
