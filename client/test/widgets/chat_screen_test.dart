import 'dart:async';
import 'dart:convert';

import 'package:cuebooth_client/screens/chat_screen.dart';
import 'package:cuebooth_client/services/chat_service.dart';
import 'package:cuebooth_client/services/session.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

void main() {
  final base = Uri.parse('http://production-pc:7878');

  /// Builds a ready session whose snapshot carries the given chat state.
  Future<Session> sessionWithChat(
    WidgetTester tester,
    Map<String, dynamic>? chat,
  ) async {
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
        'stream': {'platform': 'restream', 'chat': ?chat},
      });
      await Future<void>.delayed(Duration.zero);
    });
    await tester.pump();
    return session;
  }

  ChatService serviceReturning(http.Response Function() respond) => ChatService(
    serverBase: base,
    client: MockClient((_) async => respond()),
  );

  Future<void> pumpChat(
    WidgetTester tester, {
    required Session session,
    required ChatService chat,
    bool useWebview = false,
    List<Uri>? launched,
  }) async {
    await tester.pumpWidget(
      MaterialApp(
        home: ChatScreen(
          session: session,
          chat: chat,
          useWebview: useWebview,
          launch: (uri) async {
            launched?.add(uri);
            return true;
          },
        ),
      ),
    );
    await tester.pumpAndSettle();
  }

  testWidgets('offers a one-tap connect when authorization is needed', (
    tester,
  ) async {
    final session = await sessionWithChat(tester, {
      'provider': 'restream',
      'status': 'needs_auth',
    });
    final launched = <Uri>[];

    await pumpChat(
      tester,
      session: session,
      chat: serviceReturning(() => http.Response('{}', 409)),
      launched: launched,
    );

    expect(find.text('Connect restream'), findsOneWidget);
    await tester.tap(find.widgetWithText(FilledButton, 'Connect'));
    await tester.pump();

    // OAuth has to leave the app: providers commonly refuse to serve a login
    // inside an embedded webview.
    expect(launched.single, Uri.parse('$base/chat/auth/start'));
  });

  testWidgets('says so when no provider is configured', (tester) async {
    final session = await sessionWithChat(tester, null);

    await pumpChat(
      tester,
      session: session,
      chat: serviceReturning(() => http.Response('{}', 404)),
    );

    expect(find.text('Chat is not set up'), findsOneWidget);
    expect(find.byType(FilledButton), findsNothing);
  });

  testWidgets('offers the browser on platforms without a webview', (
    tester,
  ) async {
    const url = 'https://chat.restream.io/embed?token=abc';
    final session = await sessionWithChat(tester, {
      'provider': 'restream',
      'status': 'ready',
    });
    final launched = <Uri>[];

    await pumpChat(
      tester,
      session: session,
      chat: serviceReturning(
        () => http.Response(jsonEncode({'url': url}), 200),
      ),
      launched: launched,
    );

    expect(find.text('Open chat in your browser'), findsOneWidget);
    await tester.tap(find.widgetWithText(FilledButton, 'Open chat'));
    await tester.pump();

    // The launched URL is the server-minted one, so the operator lands in a
    // chat they are already signed in to.
    expect(launched.single, Uri.parse(url));
  });

  testWidgets('offers to reconnect when the credential lapsed after all', (
    tester,
  ) async {
    // The snapshot says ready but the server disagrees — the window between
    // the two is exactly when a credential can lapse.
    final session = await sessionWithChat(tester, {
      'provider': 'restream',
      'status': 'ready',
    });
    final launched = <Uri>[];

    await pumpChat(
      tester,
      session: session,
      chat: serviceReturning(() => http.Response('{}', 409)),
      launched: launched,
    );

    expect(find.text('Chat needs reconnecting'), findsOneWidget);
    await tester.tap(find.widgetWithText(FilledButton, 'Reconnect'));
    await tester.pump();
    expect(launched.single, Uri.parse('$base/chat/auth/start'));
  });

  // Separate tests rather than a loop in one: pumping a second ChatScreen at
  // the same position reuses the first one's State, so the stale result would
  // still be on screen.
  for (final (status, title) in [
    (502, 'Chat platform is not responding'),
    (500, 'Could not reach the server'),
  ]) {
    testWidgets('shows "$title" for status $status', (tester) async {
      final session = await sessionWithChat(tester, {
        'provider': 'restream',
        'status': 'ready',
      });

      await pumpChat(
        tester,
        session: session,
        chat: serviceReturning(() => http.Response('{}', status)),
      );

      expect(find.text(title), findsOneWidget);
      expect(find.widgetWithText(FilledButton, 'Try again'), findsOneWidget);
    });
  }

  testWidgets('retrying re-asks the server', (tester) async {
    var calls = 0;
    final session = await sessionWithChat(tester, {
      'provider': 'restream',
      'status': 'ready',
    });
    final chat = ChatService(
      serverBase: base,
      client: MockClient((_) async {
        calls++;
        return calls == 1
            ? http.Response('{}', 502)
            : http.Response(
                jsonEncode({'url': 'https://chat.restream.io/embed?token=z'}),
                200,
              );
      }),
    );

    await pumpChat(tester, session: session, chat: chat);
    expect(find.text('Chat platform is not responding'), findsOneWidget);

    await tester.tap(find.widgetWithText(FilledButton, 'Try again'));
    await tester.pumpAndSettle();

    expect(calls, 2);
    expect(find.text('Open chat in your browser'), findsOneWidget);
  });

  // Authorization finishes in a browser, so the panel has to notice the
  // server's republished status on its own.
  testWidgets('switches from the connect prompt when authorization lands', (
    tester,
  ) async {
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
          'chat': {'provider': 'restream', 'status': 'needs_auth'},
        },
      });
      await Future<void>.delayed(Duration.zero);
    });
    await tester.pump();

    await pumpChat(
      tester,
      session: session,
      chat: serviceReturning(
        () => http.Response(
          jsonEncode({'url': 'https://chat.restream.io/embed?token=q'}),
          200,
        ),
      ),
    );
    expect(find.text('Connect restream'), findsOneWidget);

    await tester.runAsync(() async {
      inbound.add({
        'type': 'state-delta',
        'rev': 2,
        'patch': {
          'stream': {
            'chat': {'status': 'ready'},
          },
        },
      });
      await Future<void>.delayed(Duration.zero);
    });
    await tester.pumpAndSettle();

    expect(find.text('Connect restream'), findsNothing);
    expect(find.text('Open chat in your browser'), findsOneWidget);
  });
}
