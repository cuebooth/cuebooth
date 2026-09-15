import 'package:cuebooth_client/panes/pane.dart';
import 'package:cuebooth_client/panes/pane_layout.dart';
import 'package:cuebooth_client/panes/pane_scaffold.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// Counts how often its state is created, so a rebuild that discards the
/// element is visible to a test. Chat's body does real work in `initState` —
/// it mints a URL, which costs the server a token rotation.
class _Counted extends StatefulWidget {
  const _Counted(this.label, this.inits);

  final String label;
  final Map<String, int> inits;

  @override
  State<_Counted> createState() => _CountedState();
}

class _CountedState extends State<_Counted> {
  @override
  void initState() {
    super.initState();
    widget.inits.update(widget.label, (n) => n + 1, ifAbsent: () => 1);
  }

  @override
  Widget build(BuildContext context) => Center(child: Text('${widget.label} body'));
}

void main() {
  late Map<String, int> inits;

  PaneSpec pane(
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
    builder: (_) => _Counted(id, inits),
  );

  Future<PaneLayout> layoutOf(List<PaneSpec> panes) async {
    SharedPreferences.setMockInitialValues({});
    final layout = PaneLayout(
      panes: panes,
      prefs: await SharedPreferences.getInstance(),
    );
    addTearDown(layout.dispose);
    return layout;
  }

  Future<void> pump(WidgetTester tester, PaneLayout layout) async {
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: PaneScaffold(layout: layout, emptyCentre: const Text('empty')),
        ),
      ),
    );
    await tester.pumpAndSettle();
  }

  setUp(() => inits = {});

  // The stack's children vary in number with what is pinned. Matched
  // positionally, a strip appearing hands a floating pane's element to a
  // different pane: its body is rebuilt from scratch and it re-enters from
  // off-screen.
  testWidgets('an unrelated pin gesture does not rebuild a floating pane', (
    tester,
  ) async {
    final layout = await layoutOf([
      pane('grid', edge: PaneEdge.left, fillsCentre: true),
      pane('chat', startsPinned: false),
    ]);
    await pump(tester, layout);
    layout.toggleSummoned('chat');
    await tester.pumpAndSettle();
    final before = inits['chat'];
    final settled = tester.getRect(find.byKey(paneKey('chat')));

    // Unpinning the centre pane adds a left tab strip, changing the stack.
    layout.togglePin('grid');
    await tester.pump();

    expect(inits['chat'], before, reason: 'the pane body was rebuilt');
    expect(
      tester.getRect(find.byKey(paneKey('chat'))).left,
      closeTo(settled.left, tabStripThickness + 1),
      reason: 'the pane jumped off-screen and re-entered',
    );
  });

  testWidgets('dismissing one floating pane leaves the other in place', (
    tester,
  ) async {
    final layout = await layoutOf([
      pane('grid', edge: PaneEdge.left, fillsCentre: true),
      pane('chat', startsPinned: false),
      pane('levels', edge: PaneEdge.bottom, startsPinned: false),
    ]);
    await pump(tester, layout);
    layout.toggleSummoned('chat');
    layout.toggleSummoned('levels');
    await tester.pumpAndSettle();
    final before = inits['chat'];
    final settled = tester.getRect(find.byKey(paneKey('chat')));

    layout.toggleSummoned('levels');
    await tester.pumpAndSettle();

    expect(inits['chat'], before);
    expect(tester.getRect(find.byKey(paneKey('chat'))), settled);
  });

  // Both toggles inside one frame leave the controller at rest, so no status
  // ever fires and the pane would stay mounted off-screen for good.
  testWidgets('a pane summoned and dismissed in one frame stops being drawn', (
    tester,
  ) async {
    final layout = await layoutOf([
      pane('grid', edge: PaneEdge.left, fillsCentre: true),
      pane('chat', startsPinned: false),
    ]);
    await pump(tester, layout);

    layout.toggleSummoned('chat');
    layout.toggleSummoned('chat');
    await tester.pumpAndSettle();

    expect(find.byKey(paneKey('chat')), findsNothing);
  });

  // Below about 44px even the pin does not fit, and a header painting outside
  // its pane throws in debug and clips un-hittably in release.
  testWidgets('a dock too narrow for a header does not overflow', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(200, 400);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);

    final layout = await layoutOf([
      pane('grid', edge: PaneEdge.left, fillsCentre: true),
      pane('chat'),
    ]);
    await pump(tester, layout);

    expect(tester.takeException(), isNull);
  });

  testWidgets('a dock too short for a header does not overflow', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(800, 175);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);

    final layout = await layoutOf([
      pane('grid', edge: PaneEdge.left, fillsCentre: true),
      pane('levels', edge: PaneEdge.bottom),
    ]);
    await pump(tester, layout);

    expect(tester.takeException(), isNull);
  });
}
