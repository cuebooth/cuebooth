import 'dart:async';

import 'package:flutter/material.dart';

import '../panes/pane.dart';
import '../panes/pane_layout.dart';
import '../panes/pane_scaffold.dart';
import '../services/chat_service.dart';
import '../services/server_connection.dart';
import '../services/session.dart';
import '../widgets/stream_control_bar.dart';
import '../widgets/surface_grid.dart';
import 'chat_screen.dart';

/// Pane ids. Stable strings: they key the persisted layout.
const String surfacePaneId = 'surface';
const String chatPaneId = 'chat';

/// The operator's main control surface.
///
/// CB-014 wires the connection/session status and surfaces session notices.
/// The panes are the Companion Satellite button grid (CB-015, [SurfaceGrid])
/// with the stream/recording status bar (CB-016, [StreamControlBar]), and
/// stream chat (CB-017), arranged by the operator (CB-103).
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
  late final PaneLayout _layout;

  @override
  void initState() {
    super.initState();
    _noticeSub = widget.session.notices.listen(_showNotice);
    _layout = PaneLayout(
      panes: [
        PaneSpec(
          id: surfacePaneId,
          title: 'Controls',
          icon: Icons.grid_view,
          edge: PaneEdge.left,
          fillsCentre: true,
          builder: (_) => Column(
            children: [
              StreamControlBar(session: widget.session),
              Expanded(child: SurfaceGrid(session: widget.session)),
            ],
          ),
        ),
        PaneSpec(
          id: chatPaneId,
          title: 'Chat',
          icon: Icons.chat_bubble_outline,
          edge: PaneEdge.right,
          builder: (_) {
            final chat = _chatService();
            if (chat == null) {
              return const Center(child: Text('Not connected.'));
            }
            return ChatScreen(
              session: widget.session,
              chat: chat,
              embedded: true,
            );
          },
        ),
      ],
    );
    unawaited(_layout.load());
    widget.session.state.addListener(_syncPaneAvailability);
    _syncPaneAvailability();
  }

  @override
  void dispose() {
    widget.session.state.removeListener(_syncPaneAvailability);
    _noticeSub?.cancel();
    _layout.dispose();
    _chat?.dispose();
    super.dispose();
  }

  /// A deployment whose server has no chat provider gets no chat pane and no
  /// tab for one, rather than a pane that can only explain its own absence.
  void _syncPaneAvailability() {
    _layout.setEnabled(chatPaneId, widget.session.state.chatConfigured);
  }

  /// The chat client, built from the same server this session is connected to.
  /// Null until a connection has a host, which is also when chat is unreachable.
  ChatService? _chatService() {
    final base = widget.connection.httpBase;
    if (base == null) return null;
    return _chat ??= ChatService(serverBase: base);
  }

  Widget _centered(String text) => Center(
    child: Padding(
      padding: const EdgeInsets.all(24),
      child: Text(text, style: const TextStyle(fontSize: 18)),
    ),
  );

  void _showNotice(SessionNotice notice) {
    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(content: Text(notice.message)),
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
          return PaneScaffold(
            layout: _layout,
            emptyCentre: const _EmptyDock(),
          );
        },
      ),
    );
  }
}

/// What the dock shows with nothing pinned to its centre. Every pane can be
/// unpinned, so this is a state the operator can always reach.
class _EmptyDock extends StatelessWidget {
  const _EmptyDock();

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return ColoredBox(
      color: theme.colorScheme.surfaceContainerLowest,
      child: Center(
        // Placeholder for the CueBooth mark.
        child: Text(
          'CueBooth',
          style: theme.textTheme.headlineMedium?.copyWith(
            color: theme.colorScheme.outlineVariant,
            letterSpacing: 2,
          ),
        ),
      ),
    );
  }
}
