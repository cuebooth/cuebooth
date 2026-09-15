import 'package:cuebooth_client/panes/pane.dart';
import 'package:cuebooth_client/panes/pane_layout.dart';
import 'package:cuebooth_client/panes/pane_scaffold.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

PaneSpec _pane(
  String id, {
  PaneEdge edge = PaneEdge.right,
  bool fillsCentre = false,
  bool startsPinned = true,
}) => PaneSpec(
  id: id,
  title: id,
  icon: Icons.square,
  edge: edge,
  fillsCentre: fillsCentre,
  startsPinned: startsPinned,
  builder: (_) => Center(child: Text('$id body')),
);

Future<PaneLayout> _layout(List<PaneSpec> panes) async {
  SharedPreferences.setMockInitialValues({});
  final layout = PaneLayout(
    panes: panes,
    prefs: await SharedPreferences.getInstance(),
  );
  addTearDown(layout.dispose);
  return layout;
}

Future<void> _pump(
  WidgetTester tester,
  PaneLayout layout, {
  Size size = const Size(800, 600),
}) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(
    MaterialApp(
      home: Scaffold(
        body: PaneScaffold(layout: layout, emptyCentre: const Text('empty')),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

/// The rendered extent of a pane along its own axis.
double _extentOf(WidgetTester tester, String id, {required bool horizontal}) {
  final size = tester.getSize(find.byKey(paneKey(id)));
  return horizontal ? size.width : size.height;
}

void main() {
  group('allocatePaneExtents', () {
    test('an ordinary request is honoured', () {
      expect(
        allocatePaneExtents(
          fractions: [0.3],
          available: 800,
          dividerCount: 1,
        ),
        [closeTo(240, 0.001)],
      );
    });

    test('a request below the usable floor is raised to it', () {
      // 0.15 of a 360px phone is 54px — narrower than the pane's own header,
      // which puts the pin outside the window.
      final extents = allocatePaneExtents(
        fractions: [minPaneFraction],
        available: 360,
        dividerCount: 1,
      );
      expect(extents.single, minPaneExtent);
    });

    test('panes asking for more than the axis holds leave the centre its floor',
        () {
      final extents = allocatePaneExtents(
        fractions: [maxPaneFraction, maxPaneFraction],
        available: 800,
        dividerCount: 2,
      );
      final total = extents.fold<double>(0, (sum, e) => sum + e);
      expect(total, lessThanOrEqualTo(800 - 2 * dividerThickness - minCentreExtent + 0.001));
    });

    test('an axis with no room for any pane allocates nothing', () {
      expect(
        allocatePaneExtents(fractions: [0.3], available: 100, dividerCount: 1),
        [0.0],
      );
    });
  });

  // Without this the rendered size ignores the stored fraction entirely and
  // every pane is the same width, while persistence tests still pass.
  testWidgets('a pinned pane renders at its stored fraction', (tester) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat'),
    ]);
    layout.setFraction('chat', 0.25);
    await _pump(tester, layout);

    expect(_extentOf(tester, 'chat', horizontal: true), closeTo(200, 0.5));

    layout.setFraction('chat', 0.5);
    await tester.pumpAndSettle();

    expect(_extentOf(tester, 'chat', horizontal: true), closeTo(400, 0.5));
  });

  testWidgets('a pinned pane never shrinks below its usable floor', (
    tester,
  ) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat'),
    ]);
    layout.setFraction('chat', minPaneFraction);
    await _pump(tester, layout, size: const Size(360, 800));

    expect(
      _extentOf(tester, 'chat', horizontal: true),
      greaterThanOrEqualTo(minPaneExtent),
    );
    // The pin is the only way back from a pane dragged small, so it has to stay
    // inside the window.
    expect(tester.getCenter(find.byTooltip('Unpin chat')).dx, lessThan(360));
  });

  group('divider drags', () {
    // A sign error here is invisible to every state-level test: the fraction
    // still changes, just the wrong way.
    testWidgets('dragging toward the centre grows a right-edge pane', (
      tester,
    ) async {
      final layout = await _layout([
        _pane('grid', edge: PaneEdge.left, fillsCentre: true),
        _pane('chat'),
      ]);
      await _pump(tester, layout);
      final before = layout.fractionOf('chat');

      await tester.drag(find.byKey(paneDividerKey('chat')), const Offset(-80, 0));
      await tester.pumpAndSettle();

      expect(layout.fractionOf('chat'), greaterThan(before));
    });

    testWidgets('dragging away from the centre grows a left-edge pane', (
      tester,
    ) async {
      final layout = await _layout([
        _pane('grid', edge: PaneEdge.right, fillsCentre: true),
        _pane('levels', edge: PaneEdge.left),
      ]);
      await _pump(tester, layout);
      final before = layout.fractionOf('levels');

      await tester.drag(find.byKey(paneDividerKey('levels')), const Offset(80, 0));
      await tester.pumpAndSettle();

      expect(layout.fractionOf('levels'), greaterThan(before));
    });

    testWidgets('dragging toward the centre grows a bottom-edge pane', (
      tester,
    ) async {
      final layout = await _layout([
        _pane('grid', edge: PaneEdge.left, fillsCentre: true),
        _pane('levels', edge: PaneEdge.bottom),
      ]);
      await _pump(tester, layout);
      final before = layout.fractionOf('levels');

      await tester.drag(find.byKey(paneDividerKey('levels')), const Offset(0, -60));
      await tester.pumpAndSettle();

      expect(layout.fractionOf('levels'), greaterThan(before));
    });

    testWidgets('dragging away from the centre grows a top-edge pane', (
      tester,
    ) async {
      final layout = await _layout([
        _pane('grid', edge: PaneEdge.left, fillsCentre: true),
        _pane('levels', edge: PaneEdge.top),
      ]);
      await _pump(tester, layout);
      final before = layout.fractionOf('levels');

      await tester.drag(find.byKey(paneDividerKey('levels')), const Offset(0, 60));
      await tester.pumpAndSettle();

      expect(layout.fractionOf('levels'), greaterThan(before));
    });
  });

  // The inset is what stops a tab sitting over the pane behind it; without it
  // the dock still renders and nothing else in the suite notices.
  testWidgets('a tab strip insets the dock rather than overlapping it', (
    tester,
  ) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat'),
    ]);
    await _pump(tester, layout);

    // Unpinning chat puts a tab strip on the right edge.
    layout.togglePin('chat');
    await tester.pumpAndSettle();

    final tab = tester.getRect(find.byKey(paneTabKey('chat')));
    final centre = tester.getRect(find.byKey(paneKey('grid')));
    expect(tab.left, greaterThanOrEqualTo(800 - tabStripThickness - 0.001));
    // The dock stops short of the strip rather than running under it.
    expect(centre.right, lessThanOrEqualTo(tab.left + 0.001));
  });
}
