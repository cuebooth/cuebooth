import 'package:flutter/material.dart';

import '../services/server_connection.dart';
import 'home_screen.dart';

/// First-launch screen for entering the server host:port and connecting.
/// Wired in the next pass (CB-014) — for now it just renders a placeholder
/// form and a single connect button that pushes the home screen.
class ConnectScreen extends StatefulWidget {
  const ConnectScreen({super.key, required this.connection});

  final ServerConnection connection;

  @override
  State<ConnectScreen> createState() => _ConnectScreenState();
}

class _ConnectScreenState extends State<ConnectScreen> {
  final _hostCtrl = TextEditingController(text: '127.0.0.1');
  final _portCtrl = TextEditingController(text: '7878');

  @override
  void dispose() {
    _hostCtrl.dispose();
    _portCtrl.dispose();
    super.dispose();
  }

  Future<void> _connect() async {
    final host = _hostCtrl.text.trim();
    final port = int.tryParse(_portCtrl.text.trim()) ?? 7878;
    await widget.connection.connect(host, port);
    if (!mounted) return;
    Navigator.of(context).push(
      MaterialPageRoute(
        builder: (_) => HomeScreen(connection: widget.connection),
      ),
    );
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
                  decoration: const InputDecoration(
                    labelText: 'Host',
                    helperText: 'LAN IP or Tailscale address',
                  ),
                ),
                const SizedBox(height: 12),
                TextField(
                  controller: _portCtrl,
                  keyboardType: TextInputType.number,
                  decoration: const InputDecoration(labelText: 'Port'),
                ),
                const SizedBox(height: 24),
                FilledButton(
                  onPressed: _connect,
                  child: const Text('Connect'),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
