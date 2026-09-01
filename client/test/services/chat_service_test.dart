import 'dart:convert';

import 'package:cuebooth_client/services/chat_service.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

void main() {
  final base = Uri.parse('http://production-pc:7878');

  ChatService serviceReturning(
    http.Response Function(http.Request) handler, {
    List<Uri>? seen,
  }) {
    return ChatService(
      serverBase: base,
      client: MockClient((request) async {
        seen?.add(request.url);
        return handler(request);
      }),
    );
  }

  group('ChatService.fetchUrl', () {
    test('returns the minted url on 200', () async {
      const url = 'https://chat.restream.io/embed?token=abc';
      final seen = <Uri>[];
      final service = serviceReturning(
        (_) => http.Response(jsonEncode({'url': url}), 200),
        seen: seen,
      );

      final result = await service.fetchUrl();

      expect(result.isReady, isTrue);
      expect(result.url, url);
      expect(seen.single, Uri.parse('http://production-pc:7878/chat/url'));
    });

    test('reports needsAuth on 409', () async {
      final service = serviceReturning(
        (_) => http.Response(
          jsonEncode({'status': 'needs_auth', 'auth_start': '/chat/auth/start'}),
          409,
        ),
      );

      final result = await service.fetchUrl();

      expect(result.isReady, isFalse);
      expect(result.error, ChatUrlError.needsAuth);
    });

    test('reports platformUnavailable on 502', () async {
      final service = serviceReturning((_) => http.Response('{}', 502));

      expect((await service.fetchUrl()).error, ChatUrlError.platformUnavailable);
    });

    test('reports unreachable when the request throws', () async {
      final service = ChatService(
        serverBase: base,
        client: MockClient((_) async => throw const _Offline()),
      );

      expect((await service.fetchUrl()).error, ChatUrlError.unreachable);
    });

    // A 200 whose body isn't the documented shape leaves nothing to display, so
    // it must not be reported as ready with a null URL.
    test('reports unreachable when a 200 carries no usable url', () async {
      for (final body in ['not json', '{}', '{"url": ""}', '[]']) {
        final service = serviceReturning((_) => http.Response(body, 200));
        final result = await service.fetchUrl();

        expect(result.isReady, isFalse, reason: 'body: $body');
        expect(result.error, ChatUrlError.unreachable, reason: 'body: $body');
      }
    });

    // A webview builds its controller during build(), where a FormatException
    // from an unparseable URL escapes as a framework error instead of the
    // panel's own message. Parsing here keeps it on the error path.
    test('rejects a url that is not an absolute http(s) URL', () async {
      for (final url in [
        'http://[bad',
        'not-a-url',
        '/chat/embed',
        'javascript:alert(1)',
        'ftp://example.test/chat',
      ]) {
        final service = serviceReturning(
          (_) => http.Response(jsonEncode({'url': url}), 200),
        );
        final result = await service.fetchUrl();

        expect(result.isReady, isFalse, reason: 'url: $url');
        expect(result.error, ChatUrlError.unreachable, reason: 'url: $url');
      }
    });

    test('reports unreachable on an unexpected status', () async {
      final service = serviceReturning((_) => http.Response('nope', 404));

      expect((await service.fetchUrl()).error, ChatUrlError.unreachable);
    });

    // Each call must reach the server: the token inside a minted URL is the
    // platform's to expire, so a cached one goes stale silently.
    test('asks the server every time rather than caching', () async {
      var calls = 0;
      final service = serviceReturning((_) {
        calls++;
        return http.Response(jsonEncode({'url': 'https://chat/$calls'}), 200);
      });

      expect((await service.fetchUrl()).url, 'https://chat/1');
      expect((await service.fetchUrl()).url, 'https://chat/2');
      expect(calls, 2);
    });
  });

  test('authStartUrl points at the server route', () {
    final service = ChatService(serverBase: base, client: MockClient((_) async => http.Response('', 200)));

    expect(
      service.authStartUrl,
      Uri.parse('http://production-pc:7878/chat/auth/start'),
    );
  });
}

class _Offline implements Exception {
  const _Offline();
}
