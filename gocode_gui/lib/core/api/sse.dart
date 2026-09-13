import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import 'models.dart';

/// A Server-Sent Events stream for gocode's `/api/event`, with reconnect.
///
/// gocode's subscriptions are buffered and **lossy by design**: a slow client
/// drops events rather than slowing the runner. So every (re)connection is
/// followed by a [SseEvents.reconnectSignal] that tells the owner to
/// reconcile state from `GET /api/session/{id}/message`.
class SseClient {
  SseClient({
    required this.baseUrl,
    http.Client? httpClient,
    this.username,
    this.password,
    this.sessionID,
    Duration initialBackoff = const Duration(seconds: 1),
    Duration maxBackoff = const Duration(seconds: 30),
  })  : _http = httpClient ?? http.Client() {
    _initialBackoff = initialBackoff;
    _maxBackoff = maxBackoff;
  }

  final String baseUrl;
  final http.Client _http;
  final String? username;
  final String? password;

  /// When set, the server filters events to this session.
  final String? sessionID;

  late final Duration _initialBackoff;
  late final Duration _maxBackoff;

  StreamSubscription<void>? _sub;
  bool _closed = false;
  int _attempts = 0;

  /// Emits every parsed event.
  final _events = StreamController<ApiEvent>.broadcast();

  /// Emits once per (re)connection establishment. The owner reconciles
  /// durable state (timelines, busy flags, permission lists) in response.
  final _reconnectSignal = StreamController<void>.broadcast();

  /// Connection state for UI display.
  final _state = StreamController<SseConnectionState>.broadcast();

  Stream<ApiEvent> get events => _events.stream;
  Stream<void> get reconnectSignal => _reconnectSignal.stream;
  Stream<SseConnectionState> get state => _state.stream;

  Uri get _uri {
    var url = '$baseUrl/api/event';
    if (sessionID != null && sessionID!.isNotEmpty) {
      url += '?sessionID=${Uri.encodeQueryComponent(sessionID!)}';
    }
    return Uri.parse(url);
  }

  Map<String, String> get _headers => {
        'Accept': 'text/event-stream',
        'Cache-Control': 'no-cache',
        if (username != null && password != null)
          'Authorization':
              'Basic ${base64Encode(utf8.encode('$username:$password'))}',
      };

  /// Starts the stream. Returns once the connection is established (or the
  /// first attempt fails); reconnection continues in the background.
  Future<void> start() async {
    if (_closed) return;

    // Completes on the first successful connect, or the first failure.
    final firstAttempt = Completer<void>();
    late final StreamSubscription sub;
    sub = state.listen((s) {
      if (!firstAttempt.isCompleted) {
        if (s == SseConnectionState.connected) {
          firstAttempt.complete();
        } else if (s == SseConnectionState.disconnected && _attempts > 0) {
          firstAttempt.completeError(StateError('event stream unavailable'));
        }
      }
    });
    unawaited(_connect());
    try {
      await firstAttempt.future.timeout(const Duration(seconds: 25));
    } finally {
      await sub.cancel();
    }
  }

  Future<void> _connect() async {
    if (_closed) return;
    var backoff = _initialBackoff;
    _attempts = 0;
    while (!_closed) {
      try {
        final request = http.Request('GET', _uri)..headers.addAll(_headers);
        final response = await _http.send(request).timeout(
              const Duration(seconds: 20),
            );

        if (response.statusCode != 200) {
          await response.stream.drain().catchError((_) {});
          throw Exception('event stream: ${response.statusCode}');
        }

        _state.add(SseConnectionState.connected);
        _reconnectSignal.add(null);

        // Parse the chunked body. Buffered lines survive chunk boundaries.
        final parser = SseParser();
        await for (final chunk in response.stream) {
          if (_closed) return;
          for (final data in parser.feed(utf8.decode(chunk))) {
            final event = _decode(data);
            if (event != null) _events.add(event);
          }
        }
        // Server closed the stream cleanly — fall through to reconnect.
        throw Exception('event stream closed');
      } catch (_) {
        if (_closed) return;
        _attempts++;
        _state.add(SseConnectionState.disconnected);
        await Future<void>.delayed(_jitter(backoff));
        backoff = _nextBackoff(backoff);
      }
    }
  }

  Duration _nextBackoff(Duration current) {
    final doubled = current * 2;
    return doubled > _maxBackoff ? _maxBackoff : doubled;
  }

  Duration _jitter(Duration base) => base;

  ApiEvent? _decode(String data) {
    try {
      final decoded = jsonDecode(data);
      if (decoded is Map<String, dynamic>) return ApiEvent.fromJson(decoded);
    } catch (_) {}
    return null;
  }

  void close() {
    _closed = true;
    _sub?.cancel();
    _events.close();
    _reconnectSignal.close();
    _state.close();
    _http.close();
  }
}

enum SseConnectionState { connecting, connected, disconnected }

/// Incremental SSE parser: handles events split across chunk boundaries,
/// CRLF line endings, multi-line `data:` fields, and comment lines.
///
/// Public for unit testing (the transport layer is covered separately).
class SseParser {
  final _buffer = StringBuffer();
  final _data = <String>[];
  bool _sawAnyField = false;

  /// Feed a chunk; returns complete event payloads.
  List<String> feed(String chunk) {
    _buffer.write(chunk);
    final out = <String>[];
    while (true) {
      final buffered = _buffer.toString();
      final newline = buffered.indexOf('\n');
      if (newline < 0) break;
      final rawLine = buffered.substring(0, newline);
      _buffer
        ..clear()
        ..write(buffered.substring(newline + 1));
      // Strip a trailing \r so CRLF endings behave like LF.
      final line = rawLine.endsWith('\r')
          ? rawLine.substring(0, rawLine.length - 1)
          : rawLine;
      if (line.isEmpty) {
        if (_sawAnyField) {
          out.add(_data.join('\n'));
          _data.clear();
          _sawAnyField = false;
        }
        continue;
      }
      if (line.startsWith(':')) continue; // comment / keep-alive
      final colon = line.indexOf(':');
      final field = colon < 0 ? line : line.substring(0, colon);
      var value = colon < 0 ? '' : line.substring(colon + 1);
      if (value.startsWith(' ')) value = value.substring(1);
      if (field == 'data') {
        _data.add(value);
        _sawAnyField = true;
      }
    }
    return out;
  }
}
