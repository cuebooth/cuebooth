import 'dart:async';

import 'package:flutter/material.dart';

import '../services/chat_service.dart';
import '../services/server_connection.dart';
import '../services/session.dart';
import '../widgets/stream_control_bar.dart';
import '../widgets/surface_grid.dart';
import 'chat_screen.dart';

/// The operator's main control surface.
///
/// CB-014 wires the connection/session status and surfaces session notices.
/// The body shows the Companion Satellite button grid (CB-015, [SurfaceGrid])
/// with the stream/recording status bar (CB-016, [StreamControlBar]) above it.
class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, required this.connection, required this.session});

  final ServerConnection connection;
  final Session session;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  StreamSubscription<SessionNotice>? _noticeSub;
  ChatService? _chat;

  @override
  void initState() {
    super.initState();
    _noticeSub = widget.session.notices.listen(_showNotice);
  }

  @override
  void dispose() {
    _noticeSub?.cancel();
    _chat?.dispose();
    super.dispose();
  }

  /// The chat client, built from the same server this session is connected to.
  /// Null until a connection has a host, which is also when chat is unreachable.
  ChatService? _chatService() {
    final base = widget.connection.httpBase;
    if (base == null) return null;
    return _chat ??= ChatService(serverBase: base);
  }

  void _openChat() {
    final chat = _chatService();
    if (chat == null) return;
    Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => ChatScreen(session: widget.session, chat: chat),
      ),
    );
  }

  Widget _centered(String text) => Center(
    child: Padding(
      padding: const EdgeInsets.all(24),
      child: Text(text, style: const TextStyle(fontSize: 18)),
    ),
  );

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
            builder: (_, _) =>
                Center(child: Text(widget.connection.state.name)),
          ),
          // Shown only when the server has a chat provider; a deployment
          // without one gets no dead button.
          ListenableBuilder(
            listenable: widget.session.state,
            builder: (_, _) {
              if (!widget.session.state.chatConfigured) {
                return const SizedBox.shrink();
              }
              final icon = const Icon(Icons.chat_bubble_outline);
              return IconButton(
                tooltip: 'Chat',
                onPressed: _openChat,
                icon: widget.session.state.chatNeedsAuth
                    // A dot rather than a number: there is one thing to do,
                    // which is authorize.
                    ? Badge(smallSize: 8, child: icon)
                    : icon,
              );
            },
          ),
          // A way back to the connect screen — otherwise a connection that never
          // recovers strands the operator here with no route to change servers.
          IconButton(
            icon: const Icon(Icons.logout),
            tooltip: 'Disconnect',
            onPressed: () {
              widget.connection.disconnect();
              Navigator.of(context).pop();
            },
          ),
        ],
      ),
      body: ListenableBuilder(
        listenable: widget.session,
        builder: (context, _) {
          final session = widget.session;
          if (session.protocolIncompatible) {
            return _centered('Incompatible server protocol.');
          }
          if (!session.ready) {
            return _centered('Waiting for server…');
          }
          return Column(
            children: [
              StreamControlBar(session: session),
              Expanded(child: SurfaceGrid(session: session)),
            ],
          );
        },
      ),
    );
  }
}
