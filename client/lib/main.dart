import 'package:flutter/material.dart';

import 'screens/connect_screen.dart';
import 'services/server_connection.dart';
import 'services/session.dart';

void main() {
  runApp(const CueBoothApp());
}

class CueBoothApp extends StatefulWidget {
  const CueBoothApp({super.key});

  @override
  State<CueBoothApp> createState() => _CueBoothAppState();
}

class _CueBoothAppState extends State<CueBoothApp> {
  final _connection = ServerConnection();
  late final Session _session = Session.forConnection(_connection);

  @override
  void initState() {
    super.initState();
    _connection.addListener(_onConnectionChanged);
  }

  // When the transport leaves `connected`, the session is no longer ready: stop
  // accepting commands and drop stale state until the next `hello`.
  void _onConnectionChanged() {
    if (_connection.state != ServerConnectionState.connected) {
      _session.handleDisconnected();
    }
  }

  @override
  void dispose() {
    _connection.removeListener(_onConnectionChanged);
    _session.dispose();
    _connection.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'CueBooth',
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: Colors.deepPurple),
        useMaterial3: true,
      ),
      home: ConnectScreen(connection: _connection, session: _session),
    );
  }
}
