import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../services/server_connection.dart';
import '../services/session.dart';
import 'home_screen.dart';

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
  final _hostCtrl = TextEditingController(text: '127.0.0.1');
  final _portCtrl = TextEditingController(text: '7878');
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

    final host = _hostCtrl.text.trim();
    final port = int.tryParse(_portCtrl.text.trim());
    if (port == null || port < 1 || port > 65535) {
      setState(() => _portError = 'Enter a port between 1 and 65535.');
      return;
    }

    setState(() {
      _portError = null;
      _connecting = true;
    });
    _pendingHost = host;
    _pendingPort = port;
    widget.connection.addListener(_onConnectionChanged);
    await widget.connection.connect(host, port);
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
