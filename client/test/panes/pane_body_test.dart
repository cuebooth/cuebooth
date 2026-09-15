import 'dart:async';

import 'package:cuebooth_client/panes/pane.dart';
import 'package:cuebooth_client/panes/pane_layout.dart';
import 'package:cuebooth_client/panes/pane_scaffold.dart';
import 'package:cuebooth_client/screens/chat_screen.dart';
import 'package:cuebooth_client/services/chat_service.dart';
import 'package:cuebooth_client/services/session.dart';
import 'package:cuebooth_client/widgets/stream_control_bar.dart';
import 'package:cuebooth_client/widgets/surface_grid.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// Every other pane test uses a placeholder body, which survives any size. The
/// bodies that ship do not: they were written for a full screen, and a pane is
/// something the operator can shrink to [minPaneExtent].
Future<Session> _session(WidgetTester tester, {required bool ready}) async {
  final inbound = StreamController<Map<String, dynamic>>();
  final session = Session(inbound: inbound.stream, outbound: (_) => true);
  addTearDown(() async {
    session.dispose();
    await inbound.close();
  });
  await tester.runAsync(() async {
    inbound.add({
      'type': 'hello',
      'proto': '1.1',
      'server_version': '0',
      'server_id': 'p',
    });
    inbound.add({
      'type': 'state',
      'rev': 1,
      'stream': {
        'platform': 'restream',
        'chat': {'status': ready ? 'ready' : 'needs_auth'},
      },
    });
    await Future<void>.delayed(Duration.zero);
  });
  await tester.pump();
  return session;
}

ChatService _chat(String body, int status) => ChatService(
  serverBase: Uri.parse('http://server:7878'),
  client: MockClient((_) async => http.Response(body, status)),
);

Future<void> _pumpChatPane(
  WidgetTester tester, {
  required Size window,
  required double fraction,
  required bool ready,
  // The centre body is the real one by default. A couple of cases exist to
  // exercise chat at sizes no target actually reaches, and the button grid has
  // its own floor well above them.
  bool realCentre = true,
}) async {
  tester.view.physicalSize = window;
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);

  final session = await _session(tester, ready: ready);
  final chat = _chat(
    ready ? '{"url":"https://chat.example/embed?token=x"}' : '{}',
    ready ? 200 : 409,
  );
  addTearDown(chat.dispose);

  SharedPreferences.setMockInitialValues({});
  final layout = PaneLayout(
    panes: [
      PaneSpec(
        id: 'grid',
        title: 'Controls',
        icon: Icons.grid_view,
        edge: PaneEdge.left,
        fillsCentre: true,
        // The body that ships, not a placeholder: a placeholder survives any
        // size, which is the whole reason this file exists.
        builder: (_) => realCentre
            ? Column(
                children: [
                  StreamControlBar(session: session),
                  Expanded(child: SurfaceGrid(session: session)),
                ],
              )
            : const SizedBox.expand(),
      ),
      PaneSpec(
        id: 'chat',
        title: 'Chat',
        icon: Icons.chat_bubble_outline,
        builder: (_) => ChatScreen(
          session: session,
          chat: chat,
          useWebview: false,
          launch: (_) async => true,
        ),
      ),
    ],
    prefs: await SharedPreferences.getInstance(),
  );
  addTearDown(layout.dispose);
  layout.setFraction('chat', fraction);

  await tester.pumpWidget(
    MaterialApp(home: Scaffold(body: PaneScaffold(layout: layout))),
  );
  await tester.pumpAndSettle();
}

void main() {
  // The default arrangement on the phone and small-split widths the README
  // claims. The centre holds the control bar, whose Go Live and Record buttons
  // are the last thing in their row — so what a too-narrow pane clips is the
  // control that starts the broadcast, and clipped puts it outside the hit test.
  for (final window in const [
    Size(320, 768), // iPad Slide Over
    Size(360, 800), // Android portrait
    Size(390, 844), // iPhone 14/15
    Size(430, 932), // iPhone Pro Max
    Size(500, 800),
  ]) {
    testWidgets('the default arrangement fits at ${window.width.toInt()}dp', (
      tester,
    ) async {
      await _pumpChatPane(
        tester,
        window: window,
        fraction: defaultPaneFraction,
        ready: false,
      );
      expect(tester.takeException(), isNull);
    });
  }

  testWidgets('the broadcast control stays inside the pane it is in', (
    tester,
  ) async {
    await _pumpChatPane(
      tester,
      window: const Size(390, 844),
      fraction: defaultPaneFraction,
      ready: false,
    );

    final go = find.widgetWithText(OutlinedButton, 'Go Live');
    expect(go, findsOneWidget);
    final pane = tester.getRect(find.byKey(paneKey('grid')));
    expect(tester.getRect(go).right, lessThanOrEqualTo(pane.right + 0.001));
  });

  testWidgets('the chat pane fits at the default arrangement', (tester) async {
    await _pumpChatPane(
      tester,
      window: const Size(800, 600),
      fraction: defaultPaneFraction,
      ready: true,
    );
    expect(tester.takeException(), isNull);
  });

  // The control being clipped here is the one that opens chat; clipped, it is
  // also outside the hit test, so the tap does nothing at all.
  testWidgets('the chat pane keeps its action reachable when ready', (
    tester,
  ) async {
    await _pumpChatPane(
      tester,
      window: const Size(800, 600),
      fraction: defaultPaneFraction,
      ready: true,
    );

    final action = find.byType(FilledButton);
    expect(action, findsOneWidget);
    final pane = tester.getRect(find.byKey(paneKey('chat')));
    expect(tester.getRect(action).bottom, lessThanOrEqualTo(pane.bottom));
  });

  testWidgets('the chat pane fits when dragged to its minimum', (tester) async {
    await _pumpChatPane(
      tester,
      window: const Size(800, 600),
      fraction: minPaneFraction,
      ready: false,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('the chat pane fits on a phone', (tester) async {
    await _pumpChatPane(
      tester,
      window: const Size(390, 844),
      fraction: defaultPaneFraction,
      ready: false,
    );
    expect(tester.takeException(), isNull);
  });

  // The reload control keeps its theme tap target whatever its icon size, so a
  // short pane is where it stops fitting — and it only exists once chat is
  // ready, which is the case the other short-window test does not reach.
  testWidgets('a ready chat pane fits in a very short window', (tester) async {
    await _pumpChatPane(
      tester,
      window: const Size(1280, 70),
      fraction: defaultPaneFraction,
      ready: true,
      realCentre: false,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('the chat pane fits in a short window', (tester) async {
    await _pumpChatPane(
      tester,
      window: const Size(1280, 400),
      fraction: defaultPaneFraction,
      ready: false,
    );
    expect(tester.takeException(), isNull);
  });

  // The connect prompt is how an operator recovers a lapsed credential, so it
  // has to survive whatever size the pane has been left at.
  testWidgets('the connect prompt stays reachable at the minimum', (
    tester,
  ) async {
    await _pumpChatPane(
      tester,
      window: const Size(800, 600),
      fraction: minPaneFraction,
      ready: false,
    );

    final connect = find.byType(FilledButton);
    expect(connect, findsOneWidget);

    // A pane this narrow cannot show the whole prompt at once, so the body
    // scrolls. What matters is that the control can still be brought into the
    // pane and tapped — clipped, it was neither.
    await tester.scrollUntilVisible(connect.first, 60);
    await tester.pumpAndSettle();

    final pane = tester.getRect(find.byKey(paneKey('chat')));
    final rect = tester.getRect(connect.first);
    expect(rect.bottom, lessThanOrEqualTo(pane.bottom + 0.001));
    expect(rect.right, lessThanOrEqualTo(pane.right + 0.001));
    await tester.tap(connect.first);
  });
}
