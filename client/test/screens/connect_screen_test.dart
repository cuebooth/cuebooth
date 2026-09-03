import 'package:cuebooth_client/screens/connect_screen.dart';
import 'package:flutter_test/flutter_test.dart';

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
}
