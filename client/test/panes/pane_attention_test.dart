import 'package:cuebooth_client/panes/pane.dart';
import 'package:cuebooth_client/panes/pane_layout.dart';
import 'package:cuebooth_client/panes/pane_scaffold.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// An unpinned pane renders nothing, so its tab is the only place a state its
/// own body would have shown — chat waiting to be authorized — can reach the
/// operator. It reaches them mid-service, which is when it matters.
void main() {
  late ValueNotifier<bool> wanted;

  PaneSpec pane(String id, {bool startsPinned = true, bool attends = false}) =>
      PaneSpec(
        id: id,
        title: id,
        icon: Icons.square,
        fillsCentre: id == 'grid',
        edge: id == 'grid' ? PaneEdge.left : PaneEdge.right,
        startsPinned: startsPinned,
        attention: attends
            ? PaneAttention(source: wanted, wanted: () => wanted.value)
            : null,
        builder: (_) => Center(child: Text('$id body')),
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
      MaterialApp(home: Scaffold(body: PaneScaffold(layout: layout))),
    );
    await tester.pumpAndSettle();
  }

  setUp(() => wanted = ValueNotifier<bool>(false));
  tearDown(() => wanted.dispose());

  testWidgets('an unpinned pane wanting attention marks its tab', (
    tester,
  ) async {
    final layout = await layoutOf([
      pane('grid'),
      pane('chat', startsPinned: false, attends: true),
    ]);
    await pump(tester, layout);
    expect(find.byType(Badge), findsNothing);

    wanted.value = true;
    await tester.pumpAndSettle();

    expect(
      find.descendant(
        of: find.byKey(paneTabKey('chat')),
        matching: find.byType(Badge),
      ),
      findsOneWidget,
    );
  });

  testWidgets('the mark clears when the pane stops wanting attention', (
    tester,
  ) async {
    final layout = await layoutOf([
      pane('grid'),
      pane('chat', startsPinned: false, attends: true),
    ]);
    await pump(tester, layout);
    wanted.value = true;
    await tester.pumpAndSettle();
    expect(find.byType(Badge), findsOneWidget);

    wanted.value = false;
    await tester.pumpAndSettle();

    expect(find.byType(Badge), findsNothing);
  });

  testWidgets('a pane with nothing to say is never marked', (tester) async {
    final layout = await layoutOf([
      pane('grid'),
      pane('chat', startsPinned: false),
    ]);
    await pump(tester, layout);

    wanted.value = true;
    await tester.pumpAndSettle();

    expect(find.byType(Badge), findsNothing);
  });
}
