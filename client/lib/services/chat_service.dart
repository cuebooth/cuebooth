import 'dart:convert';

import 'package:http/http.dart' as http;

import 'protocol.dart';

/// Why a chat URL could not be produced.
enum ChatUrlError {
  /// The server has no usable credential; an operator must authorize first.
  needsAuth,

  /// The server is reachable but the chat platform is not answering.
  platformUnavailable,

  /// The server could not be reached, or answered something unexpected.
  unreachable,
}

/// The outcome of asking the server for a chat URL.
///
/// Exactly one of [url] and [error] is set.
class ChatUrlResult {
  const ChatUrlResult.ready(this.url) : error = null;
  const ChatUrlResult.failed(this.error) : url = null;

  final String? url;
  final ChatUrlError? error;

  bool get isReady => url != null;
}

/// Fetches chat URLs from the server (protocol.md §11).
///
/// The server holds the platform credential and mints a URL per request, so
/// this deliberately does not cache: the token inside the URL is the platform's
/// to expire, and asking again is a server-side refresh rather than an operator
/// re-authorizing.
class ChatService {
  ChatService({required this.serverBase, http.Client? client})
    : _client = client ?? http.Client();

  /// The server's HTTP root, e.g. `http://production-pc:7878`.
  final Uri serverBase;
  final http.Client _client;

  /// Where an operator authorizes the chat platform. This must be opened in the
  /// system browser rather than an in-app webview: it is an OAuth login, which
  /// providers commonly refuse to serve inside embedded browsers.
  Uri get authStartUrl => serverBase.replace(path: chatAuthStartPath);

  /// Asks the server for a URL to display.
  Future<ChatUrlResult> fetchUrl() async {
    final http.Response response;
    try {
      response = await _client
          .get(serverBase.replace(path: chatURLPath))
          .timeout(const Duration(seconds: 10));
    } catch (_) {
      return const ChatUrlResult.failed(ChatUrlError.unreachable);
    }

    switch (response.statusCode) {
      case 200:
        final url = _decodeUrl(response.body);
        return url == null
            ? const ChatUrlResult.failed(ChatUrlError.unreachable)
            : ChatUrlResult.ready(url);
      case 409:
        return const ChatUrlResult.failed(ChatUrlError.needsAuth);
      case 502:
        return const ChatUrlResult.failed(ChatUrlError.platformUnavailable);
      default:
        return const ChatUrlResult.failed(ChatUrlError.unreachable);
    }
  }

  void dispose() => _client.close();

  /// Decodes the documented `{"url": ...}` body, returning null for anything
  /// else.
  ///
  /// The URL is parsed here rather than at the point of display: a webview
  /// builds its controller during `build`, where a `FormatException` from an
  /// unparseable URL would escape as a framework error instead of the panel's
  /// own "could not reach" message.
  static String? _decodeUrl(String body) {
    try {
      final decoded = jsonDecode(body);
      if (decoded is! Map<String, dynamic>) return null;
      final url = decoded['url'];
      if (url is! String || url.isEmpty) return null;

      final parsed = Uri.parse(url);
      if (!parsed.isAbsolute || (parsed.scheme != 'http' && parsed.scheme != 'https')) {
        return null;
      }
      return url;
    } on FormatException {
      // Falls through to null: a body that isn't the documented shape is
      // indistinguishable from an unreachable server as far as the UI goes.
      return null;
    }
  }
}
