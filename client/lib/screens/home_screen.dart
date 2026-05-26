import 'package:flutter/material.dart';

import '../services/server_connection.dart';

/// Placeholder main control surface. Real content (button grid, fader strips,
/// PTZ joystick, video preview, slide status) lands across CB-015, CB-025+,
/// CB-033+, CB-045, CB-062 in subsequent phases.
class HomeScreen extends StatelessWidget {
  const HomeScreen({super.key, required this.connection});

  final ServerConnection connection;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('CueBooth'),
        actions: [
          ListenableBuilder(
            listenable: connection,
            builder: (_, _) => Padding(
              padding: const EdgeInsets.only(right: 16),
              child: Center(child: Text(connection.state.name)),
            ),
          ),
        ],
      ),
      body: const Center(
        child: Text(
          'Control surface coming online in Phase 1+.',
          style: TextStyle(fontSize: 18),
        ),
      ),
    );
  }
}
