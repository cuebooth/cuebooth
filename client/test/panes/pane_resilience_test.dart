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
    MaterialApp(home: Scaffold(body: PaneScaffold(layout: layout))),
  );
  await tester.pumpAndSettle();
}

double _widthOf(WidgetTester tester, String id) =>
    tester.getSize(find.byKey(paneKey(id))).width;

void main() {
  // A centre pane has no divider, so its fraction stays at the default forever.
  // Summoned at that fraction it came back as a 30% strip of the thing it is.
  testWidgets('a summoned centre pane returns at the size it held', (
    tester,
  ) async {
    final layout = await _layout([
      _pane('grid', edge: PaneEdge.left, fillsCentre: true),
      _pane('chat'),
    ]);
    await _pump(tester, layout);
    final pinned = _widthOf(tester, 'grid');

    layout.togglePin('grid');
    await tester.pumpAndSettle();
    layout.toggleSummoned('grid');
    await tester.pumpAndSettle();

    expect(_widthOf(tester, 'grid'), greaterThanOrEqualTo(pinned));
  });

  // The ceiling assumed the other panes would settle for their floor, so a drag
  // could overrun the axis and the allocation took the difference out of a pane
  // the operator never touched.
  testWidgets('dragging one divider leaves the other pane alone', (
    tester,
  ) async {
    final layout = await _layout([
      _pane('centre', edge: PaneEdge.top, fillsCentre: true),
      _pane('a', edge: PaneEdge.left),
      _pane('b', edge: PaneEdge.right),
    ]);
    layout.setFraction('a', 0.35);
    layout.setFraction('b', 0.35);
    await _pump(tester, layout);
    final bBefore = _widthOf(tester, 'b');

    await tester.drag(find.byKey(paneDividerKey('a')), const Offset(120, 0));
    await tester.pumpAndSettle();

    expect(_widthOf(tester, 'b'), closeTo(bBefore, 0.5));
  });

  // The stored fraction must not drift past what the dock will render, or the
  // divider goes dead on the way back.
  testWidgets('a dragged fraction stays one the dock will honour', (
    tester,
  ) async {
    final layout = await _layout([
      _pane('centre', edge: PaneEdge.top, fillsCentre: true),
      _pane('a', edge: PaneEdge.left),
      _pane('b', edge: PaneEdge.right),
    ]);
    layout.setFraction('a', 0.35);
    layout.setFraction('b', 0.35);
    await _pump(tester, layout);

    await tester.drag(find.byKey(paneDividerKey('a')), const Offset(400, 0));
    await tester.pumpAndSettle();

    expect(_widthOf(tester, 'a'), closeTo(layout.fractionOf('a') * 800, 1.0));
  });
}
