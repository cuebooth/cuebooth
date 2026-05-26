import 'package:flutter/material.dart';

import 'screens/connect_screen.dart';
import 'services/server_connection.dart';

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

  @override
  void dispose() {
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
      home: ConnectScreen(connection: _connection),
    );
  }
}
