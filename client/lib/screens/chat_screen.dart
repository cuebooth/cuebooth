import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:url_launcher/url_launcher.dart';
import 'package:webview_flutter/webview_flutter.dart';

import '../services/chat_service.dart';
import '../services/session.dart';

/// Whether an in-app webview can render chat on this platform.
///
/// `webview_flutter` ships Android, iOS, and macOS implementations only — the
/// Apple one covers both because WKWebView is the same framework on each.
/// Elsewhere chat opens in the system browser, which costs the operator nothing
/// because the server hands out an already-authenticated URL (CB-093).
bool get webviewSupported {
  if (kIsWeb) return false;
  switch (defaultTargetPlatform) {
    case TargetPlatform.android:
    case TargetPlatform.iOS:
    case TargetPlatform.macOS:
      return true;
    case TargetPlatform.windows:
    case TargetPlatform.linux:
    case TargetPlatform.fuchsia:
      return false;
  }
}

/// The stream chat panel (CB-017).
///
/// The server owns the platform credential and mints a URL per request, so this
/// screen only ever renders: it never holds or refreshes a credential, and it
/// does not cache the URL it is given.
class ChatScreen extends StatefulWidget {
  const ChatScreen({
    super.key,
    required this.session,
    required this.chat,
    @visibleForTesting this.useWebview,
    @visibleForTesting this.launch,
  });

  final Session session;
  final ChatService chat;

  /// Overrides platform detection so both branches are testable off-device.
  final bool? useWebview;

  /// Overrides how external URLs are opened, for tests.
  final Future<bool> Function(Uri)? launch;

  @override
  State<ChatScreen> createState() => _ChatScreenState();
}

class _ChatScreenState extends State<ChatScreen> {
  ChatUrlResult? _result;
  bool _loading = false;

  bool get _useWebview => widget.useWebview ?? webviewSupported;

  @override
  void initState() {
    super.initState();
    // The mirrored server state is its own notifier; Session itself only
    // signals connection-level changes.
    widget.session.state.addListener(_onStateChanged);
    if (widget.session.state.chatReady) {
      _load();
    }
  }

  @override
  void dispose() {
    widget.session.state.removeListener(_onStateChanged);
    super.dispose();
  }

  /// Picks up the moment authorization completes in the browser: the server
  /// republishes the status, so the connect prompt becomes live chat without
  /// the operator reconnecting or reopening this screen.
  void _onStateChanged() {
    if (!mounted) return;
    if (widget.session.state.chatReady && _result == null && !_loading) {
      _load();
      return;
    }
    setState(() {});
  }

  Future<void> _load() async {
    setState(() => _loading = true);
    final result = await widget.chat.fetchUrl();
    if (!mounted) return;
    setState(() {
      _result = result;
      _loading = false;
    });
  }

  Future<void> _open(Uri url) async {
    final launcher = widget.launch ?? _launchExternal;
    final ok = await launcher(url);
    if (!ok && mounted) {
      ScaffoldMessenger.of(
        context,
      ).showSnackBar(const SnackBar(content: Text('Could not open a browser.')));
    }
  }

  static Future<bool> _launchExternal(Uri url) =>
      launchUrl(url, mode: LaunchMode.externalApplication);

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('Chat'),
        actions: [
          if (_result?.isReady ?? false)
            IconButton(
              icon: const Icon(Icons.refresh),
              tooltip: 'Reload chat',
              onPressed: _loading ? null : _load,
            ),
        ],
      ),
      body: _body(context),
    );
  }

  Widget _body(BuildContext context) {
    final state = widget.session.state;
    if (!state.chatConfigured) {
      return const _ChatMessage(
        icon: Icons.chat_bubble_outline,
        title: 'Chat is not set up',
        detail: 'No chat provider is configured on the server.',
      );
    }
    if (state.chatNeedsAuth) {
      return _ChatMessage(
        icon: Icons.link,
        title: 'Connect ${state.chatProvider ?? 'chat'}',
        detail:
            'Authorize CueBooth once in your browser. It stays connected from '
            'then on — you should not have to sign in again.',
        action: FilledButton.icon(
          icon: const Icon(Icons.open_in_new),
          label: const Text('Connect'),
          onPressed: () => _open(widget.chat.authStartUrl),
        ),
      );
    }
    if (_loading || _result == null) {
      return const Center(child: CircularProgressIndicator());
    }

    final result = _result!;
    if (!result.isReady) {
      return _errorBody(result.error!);
    }
    final url = result.url!;
    return _useWebview ? _ChatWebview(url: url) : _openInBrowser(url);
  }

  Widget _errorBody(ChatUrlError error) {
    switch (error) {
      case ChatUrlError.needsAuth:
        // The snapshot said ready but the server disagreed — the credential
        // lapsed between the two. Offer the same one-tap path out.
        return _ChatMessage(
          icon: Icons.link_off,
          title: 'Chat needs reconnecting',
          detail: 'CueBooth\'s access to chat has ended. Reconnect once in your browser.',
          action: FilledButton.icon(
            icon: const Icon(Icons.open_in_new),
            label: const Text('Reconnect'),
            onPressed: () => _open(widget.chat.authStartUrl),
          ),
        );
      case ChatUrlError.platformUnavailable:
        return _ChatMessage(
          icon: Icons.cloud_off,
          title: 'Chat platform is not responding',
          detail: 'The server reached out but got no answer. Streaming is unaffected.',
          action: _retryButton(),
        );
      case ChatUrlError.unreachable:
        return _ChatMessage(
          icon: Icons.wifi_off,
          title: 'Could not reach the server',
          detail: 'CueBooth could not ask the server for a chat link.',
          action: _retryButton(),
        );
    }
  }

  Widget _retryButton() => FilledButton.icon(
    icon: const Icon(Icons.refresh),
    label: const Text('Try again'),
    onPressed: _load,
  );

  Widget _openInBrowser(String url) => _ChatMessage(
    icon: Icons.open_in_browser,
    title: 'Open chat in your browser',
    detail:
        'In-app chat is not available on this platform yet, so chat opens in a '
        'browser window you can keep beside CueBooth. You are already signed in.',
    action: FilledButton.icon(
      icon: const Icon(Icons.open_in_new),
      label: const Text('Open chat'),
      onPressed: () => _open(Uri.parse(url)),
    ),
  );
}

/// Renders chat in an embedded webview.
class _ChatWebview extends StatefulWidget {
  const _ChatWebview({required this.url});

  final String url;

  @override
  State<_ChatWebview> createState() => _ChatWebviewState();
}

class _ChatWebviewState extends State<_ChatWebview> {
  late final WebViewController _controller = WebViewController()
    ..setJavaScriptMode(JavaScriptMode.unrestricted)
    ..loadRequest(Uri.parse(widget.url));

  @override
  void didUpdateWidget(_ChatWebview oldWidget) {
    super.didUpdateWidget(oldWidget);
    // A re-minted URL carries a new token, so it has to be loaded rather than
    // left showing the page the expired one produced.
    if (oldWidget.url != widget.url) {
      _controller.loadRequest(Uri.parse(widget.url));
    }
  }

  @override
  Widget build(BuildContext context) => WebViewWidget(controller: _controller);
}

/// A centered icon/title/detail block with an optional action, used for every
/// state the chat panel shows other than chat itself.
class _ChatMessage extends StatelessWidget {
  const _ChatMessage({
    required this.icon,
    required this.title,
    required this.detail,
    this.action,
  });

  final IconData icon;
  final String title;
  final String detail;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 420),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(icon, size: 44, color: theme.colorScheme.outline),
              const SizedBox(height: 16),
              Text(
                title,
                style: theme.textTheme.titleMedium,
                textAlign: TextAlign.center,
              ),
              const SizedBox(height: 8),
              Text(
                detail,
                style: theme.textTheme.bodyMedium?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
                textAlign: TextAlign.center,
              ),
              if (action != null) ...[const SizedBox(height: 24), action!],
            ],
          ),
        ),
      ),
    );
  }
}
