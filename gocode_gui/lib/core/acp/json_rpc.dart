import 'dart:async';
import 'dart:convert';

/// A JSON-RPC 2.0 error object as returned by the peer.
class JsonRpcException implements Exception {
  const JsonRpcException(this.code, this.message);

  final int code;
  final String message;

  @override
  String toString() => 'JsonRpcException($code): $message';
}

/// One notification from the peer: a method plus its params.
class JsonRpcNotification {
  const JsonRpcNotification(this.method, this.params);

  final String method;
  final Map<String, dynamic> params;
}

/// Line-delimited JSON-RPC 2.0 connection — the transport ACP uses over
/// stdio (agentclientprotocol.com/protocol/v1/transports).
///
/// Speaks both directions of the protocol surface gocode's agent needs:
///  - client→server requests with id correlation ([request]),
///  - notifications both ways ([notifications], [notify]),
///  - server→client requests ([handle]) — ACP permission asks and
///    elicitations arrive as these, and must be answered on the same wire.
///
/// Transport-agnostic on purpose: the supervisor wires a process's
/// stdin/stdout, tests wire in-memory streams.
class JsonRpcConnection {
  JsonRpcConnection({
    required Stream<String> incoming,
    required void Function(String line) send,
  }) : _send = send {
    _sub = incoming.listen(_onLine, onError: (Object e) => _fail(e), onDone: () {
      _fail(StateError('connection closed'));
    });
  }

  final void Function(String line) _send;
  StreamSubscription<String>? _sub;

  final _pending = <int, Completer<dynamic>>{};
  final _notifications = StreamController<JsonRpcNotification>.broadcast();
  final _handlers = <String, Future<Object?> Function(Map<String, dynamic>)>{};
  final _replies = <int, Completer<void>>{};

  int _nextId = 0;
  bool _closed = false;
  Object? _error;

  /// Every notification from the peer, in arrival order.
  Stream<JsonRpcNotification> get notifications => _notifications.stream;

  /// Resolves when the peer closes the connection or the transport fails.
  Future<void> get done => _doneCompleter.future;
  final _doneCompleter = Completer<void>();

  /// Registers the answerer for a server→client request method. A request
  /// with no registered handler is answered with MethodNotFound — the
  /// protocol-correct refusal, which the agent treats as a declined ask.
  void handle(
    String method,
    Future<Object?> Function(Map<String, dynamic> params) handler,
  ) {
    _handlers[method] = handler;
  }

  /// Sends a request and completes with the peer's result (or error).
  Future<dynamic> request(String method, [Object? params]) {
    if (_closed) {
      return Future.error(_error ?? StateError('connection closed'));
    }
    final id = ++_nextId;
    final completer = Completer<dynamic>();
    _pending[id] = completer;
    _write(<String, dynamic>{
      'jsonrpc': '2.0',
      'id': id,
      'method': method,
      'params': ?params,
    });
    return completer.future;
  }

  /// Sends a notification (no reply expected).
  void notify(String method, [Object? params]) {
    if (_closed) return;
    _write(<String, dynamic>{
      'jsonrpc': '2.0',
      'method': method,
      'params': ?params,
    });
  }

  void _write(Map<String, dynamic> message) {
    if (_closed) return;
    try {
      _send(jsonEncode(message));
    } catch (e) {
      _fail(e);
    }
  }

  void _onLine(String line) {
    if (_closed || line.trim().isEmpty) return;
    final Object? decoded;
    try {
      decoded = jsonDecode(line);
    } catch (_) {
      return; // A stray non-JSON line is noise, not a protocol event.
    }
    // Batches are flattened; each entry is handled on its own.
    final entries = decoded is List ? decoded : [decoded];
    for (final entry in entries) {
      if (entry is Map<String, dynamic>) _onMessage(entry);
    }
  }

  void _onMessage(Map<String, dynamic> message) {
    final id = message['id'];
    final method = message['method'];
    if (id != null && method is String) {
      _onServerRequest(id, method, message['params']);
    } else if (id != null) {
      _onResponse(id, message['result'], message['error']);
    } else if (method is String) {
      _notifications.add(
        JsonRpcNotification(method, _asObject(message['params'])),
      );
    }
  }

  void _onResponse(dynamic id, dynamic result, dynamic error) {
    final completer = _pending.remove(_asInt(id));
    if (completer == null || completer.isCompleted) return;
    if (error is Map<String, dynamic>) {
      completer.completeError(
        JsonRpcException(
          _asInt(error['code']) ?? -32000,
          error['message'] is String ? error['message'] as String : 'error',
        ),
      );
    } else {
      completer.complete(result);
    }
  }

  void _onServerRequest(dynamic id, String method, dynamic params) {
    final handler = _handlers[method];
    if (handler == null) {
      _write(<String, dynamic>{
        'jsonrpc': '2.0',
        'id': id,
        'error': {'code': -32601, 'message': 'Method not found: $method'},
      });
      return;
    }
    final reply = Completer<void>();
    _replies[_asInt(id) ?? -1] = reply;
    handler(_asObject(params)).then((result) {
      _write(<String, dynamic>{
        'jsonrpc': '2.0',
        'id': id,
        'result': result ?? <String, dynamic>{},
      });
      _finishReply(id);
    }, onError: (Object e) {
      _write(<String, dynamic>{
        'jsonrpc': '2.0',
        'id': id,
        'error': {'code': -32000, 'message': e.toString()},
      });
      _finishReply(id);
    });
  }

  void _finishReply(dynamic id) {
    _replies.remove(_asInt(id))?.complete();
  }

  static Map<String, dynamic> _asObject(dynamic value) =>
      value is Map<String, dynamic> ? value : const <String, dynamic>{};

  static int? _asInt(dynamic value) {
    if (value is int) return value;
    if (value is num) return value.toInt();
    if (value is String) return int.tryParse(value);
    return null;
  }

  void _fail(Object error) {
    if (_closed) return;
    _error = error;
    for (final completer in _pending.values) {
      if (!completer.isCompleted) completer.completeError(error);
    }
    _pending.clear();
    for (final completer in _replies.values) {
      if (!completer.isCompleted) completer.complete();
    }
    _replies.clear();
    if (!_doneCompleter.isCompleted) _doneCompleter.complete();
    _close();
  }

  /// Stops listening. Pending requests fail with a closed error.
  void close() {
    if (_closed) return;
    _error = StateError('connection closed');
    _close();
    if (!_doneCompleter.isCompleted) _doneCompleter.complete();
  }

  void _close() {
    _closed = true;
    _sub?.cancel();
    _sub = null;
  }
}
