import 'dart:async';

import 'package:flutter/material.dart';

import '../services/server_connection.dart';
import '../services/session.dart';

/// The operator's main control surface.
///
/// CB-014 wires the connection/session status and surfaces session notices.
/// The button grid (CB-015) and stream/recording controls (CB-016) fill the
/// body in subsequent commits.
class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, required this.connection, required this.session});

  final ServerConnection connection;
  final Session session;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  StreamSubscription<SessionNotice>? _noticeSub;

  @override
  void initState() {
    super.initState();
    _noticeSub = widget.session.notices.listen(_showNotice);
  }

  @override
  void dispose() {
    _noticeSub?.cancel();
    super.dispose();
  }

  void _showNotice(SessionNotice notice) {
    if (!mounted) return;
    final scheme = Theme.of(context).colorScheme;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(
        content: Text(notice.message),
        backgroundColor: notice.severity == NoticeSeverity.error
            ? scheme.errorContainer
            : null,
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('CueBooth'),
        actions: [
          ListenableBuilder(
            listenable: widget.connection,
            builder: (_, _) => Padding(
              padding: const EdgeInsets.only(right: 16),
              child: Center(child: Text(widget.connection.state.name)),
            ),
          ),
        ],
      ),
      body: ListenableBuilder(
        listenable: widget.session,
        builder: (context, _) {
          final session = widget.session;
          final String status;
          if (session.protocolIncompatible) {
            status = 'Incompatible server protocol.';
          } else if (session.ready) {
            status =
                'Connected to ${session.serverId ?? 'server'} '
                '(v${session.serverVersion ?? '?'}).';
          } else {
            status = 'Waiting for server…';
          }
          return Center(
            child: Padding(
              padding: const EdgeInsets.all(24),
              child: Text(status, style: const TextStyle(fontSize: 18)),
            ),
          );
        },
      ),
    );
  }
}
