import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../services/server_connection.dart';
import '../services/session.dart';
import 'home_screen.dart';

/// The address to prefill on the connect screen.
///
/// On the web the client is served by the server itself, so the page's own
/// origin *is* the server — prefilling it means an operator who typed the
/// address into their browser does not type it again. Everywhere else the app
/// arrived by some other route and localhost is the only sensible guess.
({String host, int port}) defaultServerAddress({bool? isWeb, Uri? base}) {
  final onWeb = isWeb ?? kIsWeb;
  final from = base ?? Uri.base;
  if (onWeb && from.host.isNotEmpty) {
    return (host: from.host, port: from.port);
  }
  return (host: '127.0.0.1', port: 7878);
}

/// Splits what the operator typed into a bare host, an optional port, and the
/// scheme if one was given.
///
/// A bare address is the ordinary case and leaves `secure` and `port` null —
/// *not stated*, rather than "insecure" and "no port" — so [useSecureScheme]
/// can fall back to the page and the caller to its own Port field. Typing a
/// scheme is how a native client, which has no page to infer from, reaches a
/// server behind a TLS front.
///
/// An address that names a scheme also settles the port: the one it carries,
/// or that scheme's default. `https://name.ts.net` means 443, the way it does
/// everywhere else — taking the Port field instead would dial 7878 against a
/// deployment that is not there.
///
/// Anything that is not one of the four recognized schemes is returned as a
/// bare host, unparsed, as is an address whose port is not a usable one.
/// `ServerConnection.connect` then rejects it the way it always has, which
/// keeps one error path for malformed input instead of two.
({String host, int? port, bool? secure}) parseServerField(String text) {
  final trimmed = text.trim();
  final uri = Uri.tryParse(trimmed);
  if (uri != null && uri.hasScheme && uri.host.isNotEmpty) {
    // http/https are accepted because the address an operator has is usually
    // the one in their browser's address bar, which carries those.
    final secure = switch (uri.scheme) {
      'wss' || 'https' => true,
      'ws' || 'http' => false,
      _ => null,
    };
    if (secure != null) {
      // Uri does not range-check a port, so this is where "host:99999" stops.
      final port = uri.hasPort ? uri.port : (secure ? 443 : 80);
      if (port >= 1 && port <= 65535) {
        return (host: uri.host, port: port, secure: secure);
      }
    }
  }
  return (host: trimmed, port: null, secure: null);
}

/// Whether the page this client was served from arrived over TLS.
///
/// Native builds have no page, so this is false there and the address decides.
bool pageIsSecure({bool? isWeb, Uri? base}) {
  final onWeb = isWeb ?? kIsWeb;
  final from = base ?? Uri.base;
  return onWeb && from.isScheme('https');
}

/// Whether to open the socket as `wss://`, given any scheme the operator typed.
///
/// A page served over HTTPS settles it on its own: a browser refuses a `ws://`
/// socket from such a page as mixed content, so `wss://` is not the preference
/// there but the only thing that can connect — hence it overrides a typed
/// `ws://` rather than honouring it and failing in the console. An HTTP page
/// imposes nothing, so a typed `wss://` still stands.
bool useSecureScheme({bool? typed, bool? isWeb, Uri? base}) {
  if (pageIsSecure(isWeb: isWeb, base: base)) return true;
  return typed ?? false;
}

/// First-launch screen for entering the server host:port and connecting.
///
/// Validates the port, then waits for the transport to actually reach
/// `connected` (or fail) before navigating — so a bad address or an unreachable
/// server surfaces here instead of dropping the operator onto a dead control
/// surface.
class ConnectScreen extends StatefulWidget {
  const ConnectScreen({
    super.key,
    required this.connection,
    required this.session,
  });

  final ServerConnection connection;
  final Session session;

  @override
  State<ConnectScreen> createState() => _ConnectScreenState();
}

class _ConnectScreenState extends State<ConnectScreen> {
  // Keys for the persisted last-good server address (CB-014 / #11).
  static const _hostPrefKey = 'server_host';
  static const _portPrefKey = 'server_port';

  // Seeded with sensible defaults; overwritten by any persisted last-good value.
  final _hostCtrl = TextEditingController(text: defaultServerAddress().host);
  final _portCtrl = TextEditingController(text: '${defaultServerAddress().port}');
  String? _portError;
  bool _connecting = false;
  // The address of the in-flight connect attempt, persisted once it succeeds.
  String? _pendingHost;
  int? _pendingPort;

  @override
  void initState() {
    super.initState();
    _loadLastGood();
  }

  // Prefill the last server we successfully connected to, so reconnecting
  // (especially to a Tailscale IP) doesn't mean re-typing it each launch.
  Future<void> _loadLastGood() async {
    final prefs = await SharedPreferences.getInstance();
    final host = prefs.getString(_hostPrefKey);
    final port = prefs.getInt(_portPrefKey);
    if (!mounted) return;
    setState(() {
      if (host != null && host.isNotEmpty) _hostCtrl.text = host;
      if (port != null) _portCtrl.text = port.toString();
    });
  }

  // Persist the address only after a connection actually succeeds, so a bad
  // entry isn't remembered as the new default.
  //
  // What is stored is what the operator typed, scheme prefix and all. Storing
  // the bare host instead would drop a typed `wss://` on the next launch and
  // reconnect a native client over `ws://`, which is the failure this screen
  // just helped them avoid.
  Future<void> _saveLastGood(String host, int port) async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString(_hostPrefKey, host);
    await prefs.setInt(_portPrefKey, port);
  }

  @override
  void dispose() {
    widget.connection.removeListener(_onConnectionChanged);
    _hostCtrl.dispose();
    _portCtrl.dispose();
    super.dispose();
  }

  Future<void> _connect() async {
    if (_connecting) return;

    final typed = parseServerField(_hostCtrl.text);
    // An address that names a scheme carries its own port, explicit or default,
    // and that wins: it is the more specific thing the operator said, and
    // pasting "https://name.ts.net" while the field still reads 7878 is the
    // obvious way to arrive here. The Port field is for a bare address.
    final port = typed.port ?? int.tryParse(_portCtrl.text.trim());
    if (port == null || port < 1 || port > 65535) {
      setState(() => _portError = 'Enter a port between 1 and 65535.');
      return;
    }

    // The Port field is left as the operator typed it. Writing the address's
    // port into it here would survive a failed attempt, so stripping the scheme
    // back off and retrying would dial the port the address implied rather than
    // the one still wanted. What worked is remembered by _saveLastGood instead.
    setState(() {
      _portError = null;
      _connecting = true;
    });
    _pendingHost = _hostCtrl.text.trim();
    _pendingPort = port;
    widget.connection.addListener(_onConnectionChanged);
    await widget.connection.connect(
      typed.host,
      port,
      secure: useSecureScheme(typed: typed.secure),
    );
    // Outcome (connected / error) is handled by _onConnectionChanged.
  }

  void _onConnectionChanged() {
    switch (widget.connection.state) {
      case ServerConnectionState.connected:
        widget.connection.removeListener(_onConnectionChanged);
        if (_pendingHost != null && _pendingPort != null) {
          _saveLastGood(_pendingHost!, _pendingPort!); // fire-and-forget
        }
        if (!mounted) return;
        setState(() => _connecting = false);
        Navigator.of(context).push(
          MaterialPageRoute(
            builder: (_) => HomeScreen(
              connection: widget.connection,
              session: widget.session,
            ),
          ),
        );
      case ServerConnectionState.error:
        // First failure: stop here, surface it, and stop the background
        // reconnect loop so it isn't retrying a bad address while the operator
        // corrects it.
        widget.connection.removeListener(_onConnectionChanged);
        final err = widget.connection.lastError ?? 'Connection failed.';
        widget.connection.disconnect();
        if (!mounted) return;
        setState(() => _connecting = false);
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Could not connect: $err')),
        );
      case ServerConnectionState.connecting:
      case ServerConnectionState.reconnecting:
      case ServerConnectionState.disconnected:
        break; // keep waiting
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('Connect to CueBooth')),
      body: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 360),
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                TextField(
                  controller: _hostCtrl,
                  autocorrect: false,
                  enabled: !_connecting,
                  decoration: const InputDecoration(
                    labelText: 'Host',
                    helperText: 'LAN IP or Tailscale address',
                  ),
                  onSubmitted: (_) => _connect(),
                ),
                const SizedBox(height: 12),
                TextField(
                  controller: _portCtrl,
                  keyboardType: TextInputType.number,
                  enabled: !_connecting,
                  decoration: InputDecoration(
                    labelText: 'Port',
                    errorText: _portError,
                  ),
                  onSubmitted: (_) => _connect(),
                ),
                const SizedBox(height: 24),
                FilledButton(
                  onPressed: _connecting ? null : _connect,
                  child: _connecting
                      ? const SizedBox(
                          height: 20,
                          width: 20,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : const Text('Connect'),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
