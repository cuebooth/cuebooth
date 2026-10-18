import 'dart:async';

import 'package:flutter/material.dart';

import '../panes/pane.dart';
import '../panes/pane_layout.dart';
import '../panes/pane_scaffold.dart';
import '../services/app_state.dart';
import '../services/chat_service.dart';
import '../services/server_connection.dart';
import '../services/session.dart';
import '../widgets/stream_control_bar.dart';
import '../widgets/surface_grid.dart';
import 'chat_screen.dart';

/// Pane ids. Stable strings: they key the persisted layout.
const String surfacePaneId = 'surface';
const String chatPaneId = 'chat';

/// Whether this deployment offers chat, remembering the last answer a server
/// actually gave.
///
/// A server with no chat provider should get no chat pane and no tab for one,
/// rather than a pane that can only explain its own absence. But mirrored state
/// is cleared on disconnect, so an absent chat topic means either that or that
/// nothing has been heard yet, and the two need telling apart in both
/// directions: reading a drop as "no chat" takes the pane and its webview out
/// of the dock on every blip, while reading it as "chat" puts a pane back on a
/// server that has none — offering to reconnect to something that was never
/// there. Only a connected server answers, so only what it says changes the
/// answer; a disconnection leaves the last one standing.
class ChatPaneAvailability {
  bool? _answered;

  bool offered(AppState state) {
    if (state.hasBaseline) _answered = state.chatConfigured;
    // Until a server has said, assume the pane belongs: a deployment with chat
    // is the case where guessing wrong costs a webview.
    return _answered ?? true;
  }
}

/// The centre pane's body: the stream controls over the button grid.
///
/// A pane is not a screen, and these two share a fixed amount of it. The bar's
/// height depends on its own width — its controls stack when narrow — so no
/// constant describes when it fits. Instead the grid is guaranteed the larger
/// share and the bar takes what is left, scrolling inside it rather than
/// pushing the grid to nothing. The grid is the product; the bar annotates it.
class SurfacePaneBody extends StatelessWidget {
  const SurfacePaneBody({super.key, required this.session});

  /// Share of the pane the bar may occupy before it starts scrolling.
  static const double barShare = 0.4;

  /// Below this the bar has no room worth giving it.
  static const double barFloor = 72;

  final Session session;

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        final room = constraints.maxHeight * barShare;
        return Column(
          children: [
            if (room >= barFloor)
              ConstrainedBox(
                constraints: BoxConstraints(maxHeight: room),
                child: SingleChildScrollView(
                  child: StreamControlBar(session: session),
                ),
              ),
            Expanded(child: SurfaceGrid(session: session)),
          ],
        );
      },
    );
  }
}

/// The operator's main control surface.
///
/// CB-014 wires the connection/session status and surfaces session notices.
/// The panes are the Companion Satellite button grid (CB-015, [SurfaceGrid])
/// with the stream/recording status bar (CB-016, [StreamControlBar]), and
/// stream chat (CB-017), arranged by the operator (CB-103).
class HomeScreen extends StatefulWidget {
  const HomeScreen({
    super.key,
    required this.connection,
    required this.session,
  });

  final ServerConnection connection;
  final Session session;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  StreamSubscription<SessionNotice>? _noticeSub;
  ChatService? _chat;
  late final PaneLayout _layout;
  final ChatPaneAvailability _chatAvailability = ChatPaneAvailability();

  /// The stored arrangement is a platform round trip away, and the defaults are
  /// not it. Building panes before it lands means building ones that are about
  /// to be replaced.
  bool _layoutLoaded = false;

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
          builder: (_) => ListenableBuilder(
            listenable: widget.session,
            builder: (_, _) => widget.session.ready
                ? SurfacePaneBody(session: widget.session)
                : _centered('Waiting for server…'),
          ),
        ),
        PaneSpec(
          id: chatPaneId,
          title: 'Chat',
          icon: Icons.chat_bubble_outline,
          edge: PaneEdge.right,
          // Unpinned, the pane renders nothing, so the prompt to authorize would
          // reach the operator only when they next summoned it — mid-service,
          // when they wanted to read chat rather than fix it.
          attention: PaneAttention(
            source: widget.session.state,
            wanted: () => widget.session.state.chatNeedsAuth,
          ),
          builder: (_) {
            final chat = _chatService();
            if (chat == null) {
              return const Center(child: Text('Not connected.'));
            }
            return ChatScreen(session: widget.session, chat: chat);
          },
        ),
      ],
    );
    // The dock is gated on this, so it has to settle either way: a read that
    // never completes would leave the operator looking at an empty dock with no
    // controls and no explanation.
    _layout.load().whenComplete(() {
      if (mounted) setState(() => _layoutLoaded = true);
    });
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

  void _syncPaneAvailability() {
    _layout.setEnabled(
      chatPaneId,
      _chatAvailability.offered(widget.session.state),
    );
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
          if (!_layoutLoaded) return const _EmptyDock();
          // The dock survives a dropped connection. Tearing it down would take
          // the chat webview with it, so a reconnect would re-mint a URL and
          // reload the page; each pane says for itself what it cannot show
          // while the session is away.
          return PaneScaffold(layout: _layout, emptyCentre: const _EmptyDock());
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
