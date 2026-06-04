import 'dart:async';

import 'package:cuebooth_client/services/session.dart';
import 'package:cuebooth_client/widgets/surface_grid.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  Future<(Session, List<Map<String, dynamic>>)> surfaceSession(
    WidgetTester tester, {
    required int rows,
    required int cols,
    List<Map<String, dynamic>> keys = const [],
  }) async {
    final inbound = StreamController<Map<String, dynamic>>();
    final sent = <Map<String, dynamic>>[];
    final session = Session(
      inbound: inbound.stream,
      outbound: (m) {
        sent.add(m);
        return true;
      },
    );
    addTearDown(() async {
      session.dispose();
      await inbound.close();
    });
    await tester.runAsync(() async {
      inbound.add({'type': 'hello', 'proto': '1.0', 'server_version': '0', 'server_id': 'p'});
      inbound.add({'type': 'surface-layout', 'rows': rows, 'cols': cols, 'bitmap_size': 72});
      for (final k in keys) {
        inbound.add(k);
      }
      await Future<void>.delayed(Duration.zero);
    });
    return (session, sent);
  }

  testWidgets('shows a waiting message before any layout', (tester) async {
    final inbound = StreamController<Map<String, dynamic>>();
    final session = Session(inbound: inbound.stream, outbound: (_) => true);
    addTearDown(() async {
      session.dispose();
      await inbound.close();
    });
    await tester.pumpWidget(
      MaterialApp(home: Scaffold(body: SurfaceGrid(session: session))),
    );
    await tester.pump();
    expect(find.textContaining('Waiting for the Companion surface'), findsOneWidget);
  });

  testWidgets('renders only configured keys as interactive cells', (tester) async {
    // 1×2 grid with a button only at index 0; index 1 stays an inert placeholder.
    final (session, _) = await surfaceSession(
      tester,
      rows: 1,
      cols: 2,
      keys: [
        {
          'type': 'surface-key',
          'key': 0,
          'seq': 1,
          'row': 0,
          'col': 0,
          'key_type': 'BUTTON',
          'pressed': false,
          'color': '#336699',
        },
      ],
    );
    await tester.pumpWidget(
      MaterialApp(home: Scaffold(body: SurfaceGrid(session: session))),
    );
    await tester.pump();
    expect(find.byType(GestureDetector), findsOneWidget);
  });

  testWidgets('a tap sends a surface key press and release', (tester) async {
    final (session, sent) = await surfaceSession(
      tester,
      rows: 1,
      cols: 1,
      keys: [
        {
          'type': 'surface-key',
          'key': 0,
          'seq': 1,
          'row': 0,
          'col': 0,
          'key_type': 'BUTTON',
          'pressed': false,
          'color': '#336699',
        },
      ],
    );
    await tester.pumpWidget(
      MaterialApp(home: Scaffold(body: SurfaceGrid(session: session))),
    );
    await tester.pump();

    await tester.tap(find.byType(GestureDetector));
    await tester.pump();

    final presses = sent.where((m) => m['type'] == 'surface-press').toList();
    expect(presses.any((m) => m['key'] == 0 && m['pressed'] == true), isTrue);
    expect(presses.any((m) => m['key'] == 0 && m['pressed'] == false), isTrue);
  });

  testWidgets('a tap emits exactly one press and one release (no duplicates)', (tester) async {
    // _setDown only emits on an actual state transition, so a single tap yields
    // exactly one down and one up — gesture callbacks that repeat a state (e.g.
    // up then cancel) must not produce duplicate surface-press frames.
    final (session, sent) = await surfaceSession(
      tester,
      rows: 1,
      cols: 1,
      keys: [
        {
          'type': 'surface-key',
          'key': 0,
          'seq': 1,
          'row': 0,
          'col': 0,
          'key_type': 'BUTTON',
          'pressed': false,
          'color': '#336699',
        },
      ],
    );
    await tester.pumpWidget(
      MaterialApp(home: Scaffold(body: SurfaceGrid(session: session))),
    );
    await tester.pump();

    await tester.tap(find.byType(GestureDetector));
    await tester.pump();

    final presses = sent.where((m) => m['type'] == 'surface-press').toList();
    expect(presses.where((m) => m['pressed'] == true).length, 1);
    expect(presses.where((m) => m['pressed'] == false).length, 1);
  });

  const oneKey = {
    'type': 'surface-key',
    'key': 0,
    'seq': 1,
    'row': 0,
    'col': 0,
    'key_type': 'BUTTON',
    'pressed': false,
    'color': '#336699',
  };

  testWidgets('a button cell is discoverable and activatable by assistive tech', (tester) async {
    final handle = tester.ensureSemantics();
    final (session, sent) = await surfaceSession(tester, rows: 1, cols: 1, keys: [oneKey]);
    await tester.pumpWidget(
      MaterialApp(home: Scaffold(body: SurfaceGrid(session: session))),
    );
    await tester.pump();

    // The cell exposes a positional semantics label (Companion bakes the real
    // label into the bitmap, so position is the best accessible name)...
    final labeled = find.bySemanticsLabel('Button row 1, column 1');
    expect(labeled, findsOneWidget);
    // ...and activating it drives a press then release.
    await tester.tap(labeled);
    await tester.pump();
    final presses = sent.where((m) => m['type'] == 'surface-press').toList();
    expect(presses.where((m) => m['pressed'] == true).length, 1);
    expect(presses.where((m) => m['pressed'] == false).length, 1);
    handle.dispose();
  });
}
