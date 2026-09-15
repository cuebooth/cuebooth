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
  builder: (_) => Text('$id body'),
);

Future<PaneLayout> _layout(List<PaneSpec> panes) async {
  SharedPreferences.setMockInitialValues({});
  return PaneLayout(panes: panes, prefs: await SharedPreferences.getInstance());
}

Future<void> _pump(WidgetTester tester, PaneLayout layout) async {
  await tester.pumpWidget(
    MaterialApp(
      home: Scaffold(
        body: PaneScaffold(
          layout: layout,
          emptyCentre: const Text('empty dock'),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('pinned panes render in the dock', (tester) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat'),
    ]);

    await _pump(tester, layout);

    expect(find.text('grid body'), findsOneWidget);
    expect(find.text('chat body'), findsOneWidget);
    expect(find.text('empty dock'), findsNothing);
  });

  testWidgets('the pin releases a pane to its tab', (tester) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat'),
    ]);
    await _pump(tester, layout);

    await tester.tap(find.byTooltip('Unpin chat'));
    await tester.pumpAndSettle();

    // Out of the dock, and not hovering over the space it left.
    expect(find.text('chat body'), findsNothing);
    // The tab is the way back to it.
    expect(find.byTooltip('Pin chat'), findsNothing);
    expect(find.text('chat'), findsOneWidget);
  });

  testWidgets('a tab summons its pane and puts it away again', (tester) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat', startsPinned: false),
    ]);
    await _pump(tester, layout);
    expect(find.text('chat body'), findsNothing);

    await tester.tap(find.text('chat'));
    await tester.pumpAndSettle();
    expect(find.text('chat body'), findsOneWidget);

    await tester.tap(find.text('chat').first);
    await tester.pumpAndSettle();
    expect(find.text('chat body'), findsNothing);
  });

  testWidgets('a summoned pane can be pinned back into the dock', (
    tester,
  ) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat', startsPinned: false),
    ]);
    await _pump(tester, layout);
    await tester.tap(find.text('chat'));
    await tester.pumpAndSettle();

    await tester.tap(find.byTooltip('Pin chat'));
    await tester.pumpAndSettle();

    expect(layout.isPinned('chat'), isTrue);
    expect(find.text('chat body'), findsOneWidget);
  });

  testWidgets('unpinning every pane leaves the branded dock', (tester) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
    ]);
    await _pump(tester, layout);
    expect(find.text('empty dock'), findsNothing);

    await tester.tap(find.byTooltip('Unpin grid'));
    await tester.pumpAndSettle();

    expect(find.text('grid body'), findsNothing);
    expect(find.text('empty dock'), findsOneWidget);
  });

  // A stack whose children are all positioned takes its size from its
  // constraints; one stray unpositioned child collapses the lot to nothing, and
  // every pane renders at zero size without anything else looking wrong.
  testWidgets('the dock fills its constraints in every arrangement', (
    tester,
  ) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat'),
      _pane('levels', edge: PaneEdge.bottom, startsPinned: false),
    ]);

    await _pump(tester, layout);
    expect(tester.getSize(find.byType(PaneScaffold)), const Size(800, 600));

    // With a tab strip present, and again with something floating over it.
    layout.togglePin('chat');
    await tester.pumpAndSettle();
    expect(tester.getSize(find.byType(PaneScaffold)), const Size(800, 600));

    layout.toggleSummoned('levels');
    await tester.pumpAndSettle();
    expect(tester.getSize(find.byType(PaneScaffold)), const Size(800, 600));
    expect(tester.getSize(find.text('grid body')).width, greaterThan(0));
  });

  // Pinning a summoned pane must clear the summons, or unpinning it later floats
  // it straight back out instead of retracting it to its tab.
  testWidgets('a pane summoned, pinned, then unpinned retracts', (tester) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat', startsPinned: false),
    ]);
    await _pump(tester, layout);

    await tester.tap(find.text('chat'));
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Pin chat'));
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Unpin chat'));
    await tester.pumpAndSettle();

    expect(layout.isVisible('chat'), isFalse);
    expect(layout.floating, isEmpty);
    expect(find.text('chat body'), findsNothing);
  });

  testWidgets('a disabled pane leaves no tab behind', (tester) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat', startsPinned: false),
    ]);
    await _pump(tester, layout);
    expect(find.text('chat'), findsOneWidget);

    layout.setEnabled('chat', false);
    await tester.pumpAndSettle();

    expect(find.text('chat'), findsNothing);
    expect(find.text('chat body'), findsNothing);
  });
}
