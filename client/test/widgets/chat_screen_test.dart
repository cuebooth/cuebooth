import 'dart:async';
import 'dart:convert';

import 'package:cuebooth_client/screens/chat_screen.dart';
import 'package:cuebooth_client/services/chat_service.dart';
import 'package:cuebooth_client/services/session.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:url_launcher/url_launcher.dart';

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

  // The server reports needs_auth for a transient refusal too, and clears it
  // after a cooldown. Without a retry here the operator's only way back is a
  // full browser sign-in for a credential that was never actually broken.
  testWidgets('the connect prompt can retry without re-authorizing', (
    tester,
  ) async {
    var calls = 0;
    final session = await sessionWithChat(tester, {
      'provider': 'restream',
      'status': 'needs_auth',
    });
    // The server's cooldown has lapsed, so the next request succeeds even
    // though the last published status still says needs_auth.
    final chat = ChatService(
      serverBase: base,
      client: MockClient((_) async {
        calls++;
        return http.Response(
          jsonEncode({'url': 'https://chat.restream.io/embed?token=c'}),
          200,
        );
      }),
    );

    await pumpChat(tester, session: session, chat: chat);
    expect(find.text('Connect restream'), findsOneWidget);
    // Nothing is fetched while the status says needs_auth.
    expect(calls, 0);

    await tester.tap(find.widgetWithText(OutlinedButton, 'Try again'));
    await tester.pumpAndSettle();

    expect(calls, 1);
    expect(find.text('Open chat in your browser'), findsOneWidget);
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

  // Authorization happens in a browser and the server republishes `ready` —
  // but if state was already `ready`, no delta is broadcast. The panel must not
  // strand the operator on the error screen with no way back.
  testWidgets('the reconnect screen also offers a retry', (tester) async {
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
            ? http.Response('{}', 409)
            : http.Response(
                jsonEncode({'url': 'https://chat.restream.io/embed?token=k'}),
                200,
              );
      }),
    );

    await pumpChat(tester, session: session, chat: chat);
    expect(find.text('Chat needs reconnecting'), findsOneWidget);

    await tester.tap(find.widgetWithText(OutlinedButton, 'Try again'));
    await tester.pumpAndSettle();

    expect(calls, 2);
    expect(find.text('Open chat in your browser'), findsOneWidget);
  });

  // The full recovery path: a mint fails, the server republishes needs_auth,
  // the operator authorizes in a browser, and the server publishes ready again.
  // The panel must reload even though it is already holding a failed result.
  testWidgets('recovers when the server republishes ready after a failure', (
    tester,
  ) async {
    final inbound = StreamController<Map<String, dynamic>>();
    final session = Session(inbound: inbound.stream, outbound: (_) => true);
    addTearDown(() async {
      session.dispose();
      await inbound.close();
    });

    Future<void> feed(Map<String, dynamic> frame) async {
      await tester.runAsync(() async {
        inbound.add(frame);
        await Future<void>.delayed(Duration.zero);
      });
      await tester.pump();
    }

    await feed({
      'type': 'hello',
      'proto': '1.1',
      'server_version': '0',
      'server_id': 'p',
    });
    await feed({
      'type': 'state',
      'rev': 1,
      'stream': {
        'chat': {'provider': 'restream', 'status': 'ready'},
      },
    });

    var calls = 0;
    final chat = ChatService(
      serverBase: base,
      client: MockClient((_) async {
        calls++;
        return calls == 1
            ? http.Response('{}', 409)
            : http.Response(
                jsonEncode({'url': 'https://chat.restream.io/embed?token=r'}),
                200,
              );
      }),
    );

    await pumpChat(tester, session: session, chat: chat);
    expect(find.text('Chat needs reconnecting'), findsOneWidget);

    // The server saw the same 409 and republished, then the operator authorized.
    await feed({
      'type': 'state-delta',
      'rev': 2,
      'patch': {
        'stream': {
          'chat': {'status': 'needs_auth'},
        },
      },
    });
    await feed({
      'type': 'state-delta',
      'rev': 3,
      'patch': {
        'stream': {
          'chat': {'status': 'ready'},
        },
      },
    });
    await tester.pumpAndSettle();

    expect(calls, 2);
    expect(find.text('Chat needs reconnecting'), findsNothing);
    expect(find.text('Open chat in your browser'), findsOneWidget);
  });

  // protocol.md §11: a client must not reuse a minted URL. One minted before a
  // revocation embeds a token that is no longer good, so returning to ready
  // has to re-mint rather than re-display.
  testWidgets('re-mints when the server returns to ready', (tester) async {
    final inbound = StreamController<Map<String, dynamic>>();
    final session = Session(inbound: inbound.stream, outbound: (_) => true);
    addTearDown(() async {
      session.dispose();
      await inbound.close();
    });

    Future<void> feed(Map<String, dynamic> frame) async {
      await tester.runAsync(() async {
        inbound.add(frame);
        await Future<void>.delayed(Duration.zero);
      });
      await tester.pump();
    }

    await feed({
      'type': 'hello',
      'proto': '1.1',
      'server_version': '0',
      'server_id': 'p',
    });
    await feed({
      'type': 'state',
      'rev': 1,
      'stream': {
        'chat': {'provider': 'restream', 'status': 'ready'},
      },
    });

    var mints = 0;
    final chat = ChatService(
      serverBase: base,
      client: MockClient((_) async {
        mints++;
        return http.Response(
          jsonEncode({'url': 'https://chat.restream.io/embed?token=mint$mints'}),
          200,
        );
      }),
    );

    await pumpChat(tester, session: session, chat: chat);
    expect(mints, 1);

    // The credential is revoked and re-granted while the panel stays open.
    await feed({
      'type': 'state-delta',
      'rev': 2,
      'patch': {
        'stream': {
          'chat': {'status': 'needs_auth'},
        },
      },
    });
    await feed({
      'type': 'state-delta',
      'rev': 3,
      'patch': {
        'stream': {
          'chat': {'status': 'ready'},
        },
      },
    });
    await tester.pumpAndSettle();

    expect(mints, 2, reason: 'the panel re-displayed a URL minted before the revocation');
  });

  // Every state change notifies the panel — viewer counts, scenes, meters — and
  // each mint costs the server a token rotation. Only a transition into ready
  // may re-fetch.
  testWidgets('unrelated state changes do not re-mint', (tester) async {
    final inbound = StreamController<Map<String, dynamic>>();
    final session = Session(inbound: inbound.stream, outbound: (_) => true);
    addTearDown(() async {
      session.dispose();
      await inbound.close();
    });

    Future<void> feed(Map<String, dynamic> frame) async {
      await tester.runAsync(() async {
        inbound.add(frame);
        await Future<void>.delayed(Duration.zero);
      });
      await tester.pump();
    }

    await feed({
      'type': 'hello',
      'proto': '1.1',
      'server_version': '0',
      'server_id': 'p',
    });
    await feed({
      'type': 'state',
      'rev': 1,
      'obs': {'scene': 'a', 'streaming': true, 'recording': false},
      'stream': {
        'chat': {'provider': 'restream', 'status': 'ready'},
      },
    });

    // The first mint fails, so the panel holds a failed result while the server
    // still says ready — the state in which re-fetching on every notification
    // turns into one token rotation per unrelated delta.
    var mints = 0;
    final chat = ChatService(
      serverBase: base,
      client: MockClient((_) async {
        mints++;
        return http.Response('{}', 409);
      }),
    );

    await pumpChat(tester, session: session, chat: chat);
    expect(mints, 1);

    for (var rev = 2; rev <= 11; rev++) {
      await feed({
        'type': 'state-delta',
        'rev': rev,
        'patch': {
          'obs': {'uptime_seconds': rev * 10},
        },
      });
    }
    await tester.pumpAndSettle();

    expect(mints, 1, reason: 'unrelated deltas triggered $mints mints, each a token rotation');
  });

  // The Linux and Windows launchers throw rather than returning false, and
  // those are exactly the platforms that can only reach chat through a browser.
  // An uncaught throw leaves the tap looking like it did nothing.
  testWidgets('a launcher that throws still reports failure', (tester) async {
    final session = await sessionWithChat(tester, {
      'provider': 'restream',
      'status': 'needs_auth',
    });

    await tester.pumpWidget(
      MaterialApp(
        home: ChatScreen(
          session: session,
          chat: serviceReturning(() => http.Response('{}', 409)),
          useWebview: false,
          launch: (_) async => throw Exception('no browser available'),
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.widgetWithText(FilledButton, 'Connect'));
    await tester.pumpAndSettle();

    expect(find.text('Could not open a browser.'), findsOneWidget);
  });

  group('openExternally', () {
    // chatNavigationStaysInPanel treats a URL it cannot parse as leaving the
    // panel, and Uri.tryParse returns null in exactly the cases Uri.parse
    // throws in — so this is reached with the strings that throw.
    test('drops a URL that cannot be parsed', () {
      for (final url in ['http://[::1', 'ht tp://x', '://nohost']) {
        expect(Uri.tryParse(url), isNull, reason: '$url should be unparseable');
        expect(() => Uri.parse(url), throwsFormatException);

        var launched = false;
        openExternally(
          url,
          launch: (uri, {LaunchMode mode = LaunchMode.platformDefault}) async {
            launched = true;
            return true;
          },
        );
        expect(launched, isFalse, reason: 'launched $url anyway');
      }
    });

    test('hands a parseable URL to the launcher', () {
      Uri? seen;
      LaunchMode? seenMode;
      openExternally(
        'https://example.test/some-link',
        launch: (uri, {LaunchMode mode = LaunchMode.platformDefault}) async {
          seen = uri;
          seenMode = mode;
          return true;
        },
      );
      expect(seen, Uri.parse('https://example.test/some-link'));
      // Externally, so the link leaves the panel rather than replacing it.
      expect(seenMode, LaunchMode.externalApplication);
    });

    // A launcher that rejects — no handler for the scheme, or a blocked link —
    // must not leave an unhandled error behind it. The rejection arrives on a
    // later microtask, so the zone has to be given the chance to see it.
    test('swallows a launcher failure', () async {
      Object? unhandled;
      await runZonedGuarded(
        () async {
          openExternally(
            'https://example.test/x',
            launch: (uri, {LaunchMode mode = LaunchMode.platformDefault}) async =>
                throw StateError('no handler'),
          );
          await Future<void>.delayed(Duration.zero);
        },
        (error, stack) => unhandled = error,
      );
      expect(unhandled, isNull);
    });
  });

  group('chatNavigationStaysInPanel', () {
    const panel = 'https://chat.restream.io/embed?token=abc';

    test('keeps navigations on the panel host', () {
      expect(
        chatNavigationStaysInPanel(
          requestUrl: 'https://chat.restream.io/embed?token=def',
          panelUrl: panel,
          isMainFrame: true,
        ),
        isTrue,
      );
    });

    test('sends a link to another host out to the browser', () {
      expect(
        chatNavigationStaysInPanel(
          requestUrl: 'https://example.test/some-link',
          panelUrl: panel,
          isMainFrame: true,
        ),
        isFalse,
      );
    });

    // WKWebView reports subframes through the same callback. Diverting them
    // breaks the embedded analytics and reCAPTCHA frames chat pages load, and
    // throws the operator into a browser mid-service.
    test('never diverts a subframe, whatever its host', () {
      for (final url in [
        'https://www.google.com/recaptcha/api2/anchor',
        'about:blank',
        'https://cdn.segment.com/analytics.js',
      ]) {
        expect(
          chatNavigationStaysInPanel(
            requestUrl: url,
            panelUrl: panel,
            isMainFrame: false,
          ),
          isTrue,
          reason: url,
        );
      }
    });

    // The host comes from the minted URL, so a second provider is not thrown
    // out of the panel the moment it loads.
    test('follows the panel URL rather than a fixed host', () {
      expect(
        chatNavigationStaysInPanel(
          requestUrl: 'https://chat.example-platform.test/embed',
          panelUrl: 'https://chat.example-platform.test/embed?token=x',
          isMainFrame: true,
        ),
        isTrue,
      );
    });

    test('a hostless main-frame url does not stay', () {
      expect(
        chatNavigationStaysInPanel(
          requestUrl: 'about:blank',
          panelUrl: panel,
          isMainFrame: true,
        ),
        isFalse,
      );
    });
  });

  // State is cleared on disconnect, so an unconfigured server and a dropped
  // connection look alike from the topic alone. Saying chat is not set up
  // during a Wi-Fi hiccup is a claim about the server that isn't true.
  testWidgets('a dropped connection reads as reconnecting, not unconfigured', (
    tester,
  ) async {
    final session = await sessionWithChat(tester, {
      'provider': 'restream',
      'status': 'needs_auth',
    });

    await pumpChat(
      tester,
      session: session,
      chat: serviceReturning(() => http.Response('{}', 409)),
    );
    expect(find.text('Connect restream'), findsOneWidget);

    await tester.runAsync(() async {
      session.state.reset();
      await Future<void>.delayed(Duration.zero);
    });
    await tester.pumpAndSettle();

    expect(find.text('Reconnecting'), findsOneWidget);
    expect(find.text('Chat is not set up'), findsNothing);
  });

  // protocol.md §1 requires a client to keep working against a newer minor
  // version. An unknown status must not leave a spinner with no way out.
  testWidgets('an unrecognised status shows a message, not a spinner', (
    tester,
  ) async {
    final session = await sessionWithChat(tester, {
      'provider': 'restream',
      'status': 'something-a-future-server-sends',
    });

    await pumpChat(
      tester,
      session: session,
      chat: serviceReturning(() => http.Response('{}', 409)),
    );

    expect(find.byType(CircularProgressIndicator), findsNothing);
    expect(find.text('Chat is unavailable'), findsOneWidget);
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
      // Both of these can persist for reasons only re-authorizing clears — a
      // revoked application, a missing scope — so a retry must not be the
      // operator's only option.
      expect(find.widgetWithText(OutlinedButton, 'Reconnect'), findsOneWidget);
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
