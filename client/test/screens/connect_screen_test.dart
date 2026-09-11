import 'package:cuebooth_client/screens/connect_screen.dart';
import 'package:cuebooth_client/services/server_connection.dart';
import 'package:cuebooth_client/services/session.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  group('defaultServerAddress', () {
    // On the web the server serves the client, so the page's own origin is the
    // server's address. Making the operator retype what they just typed into
    // the browser is the difference between "open a URL" and "set it up".
    test('on web, prefills the origin the page came from', () {
      final addr = defaultServerAddress(
        isWeb: true,
        base: Uri.parse('http://production-pc.tailnet.ts.net:7878/'),
      );

      expect(addr.host, 'production-pc.tailnet.ts.net');
      expect(addr.port, 7878);
    });

    // A server reached on the default HTTP port has no port in the URL; the
    // client still has to connect to 80, not to 7878.
    test('on web, an implicit port is the scheme default', () {
      final addr = defaultServerAddress(
        isWeb: true,
        base: Uri.parse('http://cuebooth.example/'),
      );

      expect(addr.host, 'cuebooth.example');
      expect(addr.port, 80);
    });

    // A native build arrived by some other route, so the page URL says nothing
    // about where the server is.
    test('off web, falls back to localhost', () {
      final addr = defaultServerAddress(
        isWeb: false,
        base: Uri.parse('http://production-pc:7878/'),
      );

      expect(addr.host, '127.0.0.1');
      expect(addr.port, 7878);
    });

    // Called with no arguments it reads kIsWeb and Uri.base, which is how the
    // widget actually calls it. kIsWeb is false in a VM test, so this pins the
    // native default and, with it, that the arguments are defaults rather than
    // the only path through the function.
    test('with no arguments, a native build gets localhost', () {
      final addr = defaultServerAddress();

      expect(addr.host, '127.0.0.1');
      expect(addr.port, 7878);
    });

    // A web build opened from a file: URL has no host to derive an address
    // from, so the origin is no better a guess than localhost.
    test('a hostless base falls back to localhost', () {
      final addr = defaultServerAddress(
        isWeb: true,
        base: Uri.parse('file:///home/operator/'),
      );

      expect(addr.host, '127.0.0.1');
      expect(addr.port, 7878);
    });
  });

  group('parseServerField', () {
    // The ordinary case, and the one acceptance criterion 3 protects: a bare
    // address says nothing about the scheme, so `secure` is null rather than
    // false and the page gets to decide.
    test('a bare address states no scheme', () {
      final parsed = parseServerField('production-pc');

      expect(parsed.host, 'production-pc');
      expect(parsed.port, isNull);
      expect(parsed.secure, isNull);
    });

    test('surrounding whitespace is ignored', () {
      expect(parseServerField('  production-pc  ').host, 'production-pc');
    });

    // Typing a scheme is the only way a native client, which has no page to
    // infer from, can reach a server behind a TLS front.
    test('wss:// is honoured', () {
      final parsed = parseServerField('wss://pc.tailnet.ts.net');

      expect(parsed.host, 'pc.tailnet.ts.net');
      expect(parsed.secure, isTrue);
    });

    // The address an operator has to hand is usually their browser's, which
    // carries https rather than wss.
    test('https:// means the same thing', () {
      expect(parseServerField('https://pc.tailnet.ts.net').secure, isTrue);
    });

    test('ws:// and http:// state insecure explicitly', () {
      expect(parseServerField('ws://192.168.1.50').secure, isFalse);
      expect(parseServerField('http://192.168.1.50').secure, isFalse);
    });

    test('a port in the address is picked up', () {
      final parsed = parseServerField('wss://pc.tailnet.ts.net:8443');

      expect(parsed.host, 'pc.tailnet.ts.net');
      expect(parsed.port, 8443);
      expect(parsed.secure, isTrue);
    });

    // Without this the Port field decides, and the documented native-behind-TLS
    // address dials 7878 against a deployment that only listens on 443.
    test('a scheme with no port means that scheme\'s default', () {
      expect(parseServerField('wss://pc.tailnet.ts.net').port, 443);
      expect(parseServerField('https://pc.tailnet.ts.net').port, 443);
      expect(parseServerField('ws://192.168.1.50').port, 80);
      expect(parseServerField('http://192.168.1.50').port, 80);
    });

    // A bare address says nothing about the port either, so the Port field
    // still governs it.
    test('a bare address leaves the port unstated', () {
      expect(parseServerField('production-pc').port, isNull);
    });

    // Uri range-checks nothing, so an unusable port would otherwise reach the
    // Port field's validator and report the error against a field that is fine.
    test('an out-of-range port makes the whole address malformed', () {
      for (final text in [
        'https://name.ts.net:99999',
        'https://name.ts.net:0',
      ]) {
        final parsed = parseServerField(text);

        expect(parsed.host, text, reason: 'should fall through whole');
        expect(parsed.port, isNull);
        expect(parsed.secure, isNull);
      }
    });

    test('the port bounds themselves are accepted', () {
      expect(parseServerField('wss://name.ts.net:1').port, 1);
      expect(parseServerField('wss://name.ts.net:65535').port, 65535);
    });

    // Pasting a URL out of an address bar brings a trailing slash with it.
    test('a trailing slash or path is discarded', () {
      final parsed = parseServerField('https://pc.tailnet.ts.net/');

      expect(parsed.host, 'pc.tailnet.ts.net');
      expect(parsed.secure, isTrue);
    });

    // Left whole for ServerConnection.connect to reject, so malformed input
    // has one error path rather than two.
    test('an unrecognized scheme is left as a bare host', () {
      final parsed = parseServerField('ftp://pc');

      expect(parsed.host, 'ftp://pc');
      expect(parsed.secure, isNull);
    });

    // "localhost:7878" in the host field parses as scheme "localhost" with no
    // host, which is not an address; it stays whole and is rejected later.
    test('a host:port typed into the host field is left whole', () {
      final parsed = parseServerField('localhost:7878');

      expect(parsed.host, 'localhost:7878');
      expect(parsed.secure, isNull);
    });
  });

  group('useSecureScheme', () {
    test('an https page connects over wss', () {
      final secure = useSecureScheme(
        isWeb: true,
        base: Uri.parse('https://pc.tailnet.ts.net/'),
      );

      expect(secure, isTrue);
    });

    test('an http page is unchanged', () {
      final secure = useSecureScheme(
        isWeb: true,
        base: Uri.parse('http://production-pc:7878/'),
      );

      expect(secure, isFalse);
    });

    test('a native client at a bare address uses ws', () {
      expect(
        useSecureScheme(isWeb: false, base: Uri.parse('file:///app/')),
        isFalse,
      );
    });

    // A ws:// socket from an https page is refused by the browser as mixed
    // content, so the page overrides rather than honouring a typed scheme that
    // cannot connect from there.
    test('an https page overrides a typed ws://', () {
      final secure = useSecureScheme(
        typed: false,
        isWeb: true,
        base: Uri.parse('https://pc.tailnet.ts.net/'),
      );

      expect(secure, isTrue);
    });

    // The reverse is not true: an http page imposes nothing, and wss from it
    // is allowed, so a typed scheme still stands.
    test('an http page still honours a typed wss://', () {
      final secure = useSecureScheme(
        typed: true,
        isWeb: true,
        base: Uri.parse('http://production-pc:7878/'),
      );

      expect(secure, isTrue);
    });

    test('a native client honours a typed wss://', () {
      final secure = useSecureScheme(
        typed: true,
        isWeb: false,
        base: Uri.parse('file:///app/'),
      );

      expect(secure, isTrue);
    });
  });

  // What is persisted as the last-good address is the typed text, so the
  // prefill round-trips a scheme instead of quietly dropping to ws:// on the
  // next launch.
  group('last-good address round-trip', () {
    test('a stored wss:// prefix still selects TLS when re-parsed', () {
      const stored = 'wss://pc.tailnet.ts.net';

      final parsed = parseServerField(stored);

      expect(parsed.host, 'pc.tailnet.ts.net');
      expect(useSecureScheme(typed: parsed.secure, isWeb: false), isTrue);
    });

    test('a stored bare host from an older install still works', () {
      final parsed = parseServerField('production-pc');

      expect(parsed.host, 'production-pc');
      expect(useSecureScheme(typed: parsed.secure, isWeb: false), isFalse);
    });
  });

  // The screen is where the parts meet: what is typed has to arrive at the
  // transport as a bare host plus a scheme, not as one string.
  group('ConnectScreen wiring', () {
    // Records what the transport was asked to dial. Throwing stands in for a
    // socket: `connect` catches a synchronous factory failure, so the attempt
    // ends in `error` without needing a live server.
    ({Widget widget, List<Uri> dialled}) screen() {
      final dialled = <Uri>[];
      final conn = ServerConnection(
        connectChannel: (uri) {
          dialled.add(uri);
          throw StateError('no socket in this test');
        },
      );
      addTearDown(conn.dispose);
      final session = Session(inbound: conn.messages, outbound: conn.send);
      addTearDown(session.dispose);
      return (
        widget: MaterialApp(
          home: ConnectScreen(connection: conn, session: session),
        ),
        dialled: dialled,
      );
    }

    Future<void> connectWith(WidgetTester tester, String address) async {
      SharedPreferences.setMockInitialValues({});
      await tester.enterText(find.byType(TextField).first, address);
      await tester.tap(find.text('Connect'));
      await tester.pump();
      // Let the failure snack bar come and go, so no timer outlives the test.
      await tester.pumpAndSettle(const Duration(seconds: 5));
    }

    testWidgets('a typed wss:// address dials wss with a bare host', (
      tester,
    ) async {
      SharedPreferences.setMockInitialValues({});
      final s = screen();
      await tester.pumpWidget(s.widget);
      await tester.pump();

      await connectWith(tester, 'wss://pc.tailnet.ts.net');

      expect(s.dialled.single.scheme, 'wss');
      expect(s.dialled.single.host, 'pc.tailnet.ts.net');
      expect(s.dialled.single.path, '/ws');
    });

    testWidgets('a bare address still dials ws://', (tester) async {
      SharedPreferences.setMockInitialValues({});
      final s = screen();
      await tester.pumpWidget(s.widget);
      await tester.pump();

      await connectWith(tester, 'production-pc');

      expect(s.dialled.single.scheme, 'ws');
      expect(s.dialled.single.host, 'production-pc');
    });

    // The address from development.md §3.1, typed on a fresh client whose Port
    // field still reads 7878. Before the port defaulted with the scheme this
    // dialled wss://pc.tailnet.ts.net:7878/ws, where nothing is listening.
    testWidgets('a typed wss:// address with no port reaches 443', (
      tester,
    ) async {
      SharedPreferences.setMockInitialValues({});
      final s = screen();
      await tester.pumpWidget(s.widget);
      await tester.pump();

      await connectWith(tester, 'wss://pc.tailnet.ts.net');

      expect(s.dialled.single.toString(), 'wss://pc.tailnet.ts.net/ws');
      // ...and the field says so, rather than still reading 7878.
      expect(find.widgetWithText(TextField, '443'), findsOneWidget);
    });

    testWidgets('a port in the address overrides the Port field', (
      tester,
    ) async {
      SharedPreferences.setMockInitialValues({});
      final s = screen();
      await tester.pumpWidget(s.widget);
      await tester.pump();

      await connectWith(tester, 'wss://pc.tailnet.ts.net:8443');

      expect(s.dialled.single.port, 8443);
      // ...and the field is corrected, rather than showing a port nothing used.
      expect(find.widgetWithText(TextField, '8443'), findsOneWidget);
    });
  });
}
