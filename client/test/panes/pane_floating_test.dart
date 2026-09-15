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

void main() {
  group('floatingPaneExtent', () {
    test('an ordinary request is honoured', () {
      expect(floatingPaneExtent(0.3, 800), closeTo(240, 0.001));
    });

    // The pinned path has carried this floor from the start; the floating path
    // applying the raw fraction let the same pane render narrower than its own
    // header, with the pin clipped off the end of it.
    test('a request below the usable floor is raised to it', () {
      expect(floatingPaneExtent(minPaneFraction, 360), minPaneExtent);
    });

    test('a pane never covers more than the axis holds', () {
      expect(floatingPaneExtent(0.9, 100), 100);
    });
  });

  testWidgets('a summoned pane is no narrower than its own header', (
    tester,
  ) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat', startsPinned: false),
    ]);
    layout.setFraction('chat', minPaneFraction);
    await _pump(tester, layout, size: const Size(360, 780));

    layout.toggleSummoned('chat');
    await tester.pumpAndSettle();

    expect(
      tester.getSize(find.byKey(paneKey('chat'))).width,
      greaterThanOrEqualTo(minPaneExtent),
    );
    // Nothing overflowed while rendering it.
    expect(tester.takeException(), isNull);
  });

  // The operator asked for a pane that pans away, not one that vanishes.
  testWidgets('a dismissed pane travels out before it stops being drawn', (
    tester,
  ) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat', startsPinned: false),
    ]);
    await _pump(tester, layout);
    layout.toggleSummoned('chat');
    await tester.pumpAndSettle();
    final settled = tester.getTopLeft(find.byKey(paneKey('chat')));

    layout.toggleSummoned('chat');
    await tester.pump();
    await tester.pump(paneTransition ~/ 2);

    // Still on screen, and on its way back to the right edge.
    expect(find.byKey(paneKey('chat')), findsOneWidget);
    expect(
      tester.getTopLeft(find.byKey(paneKey('chat'))).dx,
      greaterThan(settled.dx),
    );

    await tester.pumpAndSettle();
    expect(find.byKey(paneKey('chat')), findsNothing);
  });

  testWidgets('a pane summoned again mid-exit comes back', (tester) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat', startsPinned: false),
    ]);
    await _pump(tester, layout);
    layout.toggleSummoned('chat');
    await tester.pumpAndSettle();

    layout.toggleSummoned('chat');
    await tester.pump(paneTransition ~/ 3);
    layout.toggleSummoned('chat');
    await tester.pumpAndSettle();

    expect(find.byKey(paneKey('chat')), findsOneWidget);
    expect(find.text('chat body'), findsOneWidget);
  });

  // A full-height side strip painted over the ends of a top or bottom strip and
  // took the taps meant for it.
  testWidgets('perpendicular tab strips do not overlap', (tester) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.right, fillsCentre: true),
      _pane('levels', edge: PaneEdge.top, startsPinned: false),
      _pane('chat', edge: PaneEdge.left, startsPinned: false),
    ]);
    await _pump(tester, layout);

    final top = tester.getRect(find.byKey(paneTabKey('levels')));
    final side = tester.getRect(find.byKey(paneTabKey('chat')));
    expect(side.top, greaterThanOrEqualTo(top.bottom - 0.001));
  });

  testWidgets('a strip with more tabs than fit scrolls rather than overflowing',
      (tester) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.right, fillsCentre: true),
      for (var i = 0; i < 12; i++)
        _pane('pane$i', edge: PaneEdge.bottom, startsPinned: false),
    ]);

    await _pump(tester, layout);

    expect(tester.takeException(), isNull);
    expect(find.byType(SingleChildScrollView), findsWidgets);
  });
}
