import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import 'account.dart';
import 'models.dart';

/// Error shape returned by the gocode server: `{"error": "message"}`.
class ApiException implements Exception {
  ApiException(this.status, this.message);

  final int status;
  final String message;

  @override
  String toString() => 'ApiException($status): $message';
}

/// Typed REST client for the gocode HTTP API.
///
/// Mirrors the endpoint set served by internal/server/server.go; wire shapes
/// follow internal/tui/client/client.go (the TUI's own client).
class GocodeClient {
  GocodeClient({
    required this.baseUrl,
    http.Client? httpClient,
    this.username,
    this.password,
  }) : _http = httpClient ?? http.Client();

  /// e.g. `http://127.0.0.1:4096` (no trailing slash).
  final String baseUrl;
  final http.Client _http;

  /// Optional basic auth for remote attach.
  final String? username;
  final String? password;

  Map<String, String> get _headers => {
    'Accept': 'application/json',
    if (username != null && password != null)
      'Authorization':
          'Basic ${base64Encode(utf8.encode('$username:$password'))}',
  };

  String _url(String path) => baseUrl + path;

  // ---------------------------------------------------------------- plumbing

  Future<Map<String, dynamic>> _getJson(String path) async {
    final res = await _http.get(Uri.parse(_url(path)), headers: _headers);
    return _decodeObject(res, path);
  }

  Future<List<dynamic>> _getJsonList(String path) async {
    final res = await _http.get(Uri.parse(_url(path)), headers: _headers);
    return _decodeList(res, path);
  }

  Future<Map<String, dynamic>> _send(
    String method,
    String path, [
    Object? body,
  ]) async {
    final request = http.Request(method, Uri.parse(_url(path)));
    request.headers.addAll(_headers);
    if (body != null) {
      request.headers['Content-Type'] = 'application/json';
      request.body = jsonEncode(body);
    }
    final res = await _http.send(request);
    final text = await res.stream.bytesToString();
    return _decodeObject(http.Response(text, res.statusCode), path);
  }

  Future<Map<String, dynamic>> _decodeObject(
    http.Response res,
    String path,
  ) async {
    if (res.statusCode < 200 || res.statusCode >= 300) {
      throw ApiException(res.statusCode, _errorMessage(res.body, path));
    }
    final decoded = jsonDecode(res.body);
    if (decoded is Map<String, dynamic>) return decoded;
    throw ApiException(res.statusCode, 'unexpected response for $path');
  }

  Future<List<dynamic>> _decodeList(http.Response res, String path) async {
    if (res.statusCode < 200 || res.statusCode >= 300) {
      throw ApiException(res.statusCode, _errorMessage(res.body, path));
    }
    final decoded = jsonDecode(res.body);
    if (decoded is List) return decoded;
    throw ApiException(res.statusCode, 'unexpected response for $path');
  }

  String _errorMessage(String body, String path) {
    try {
      final decoded = jsonDecode(body);
      if (decoded is Map<String, dynamic>) {
        final error = decoded['error'];
        if (error is String && error.isNotEmpty) return error;
      }
    } catch (_) {}
    return body.isEmpty ? '$path failed' : body;
  }

  // ---------------------------------------------------------------- lifecycle

  Future<bool> health() async {
    try {
      final res = await _http.get(
        Uri.parse(_url('/api/health')),
        headers: _headers,
      );
      return res.statusCode == 200;
    } catch (_) {
      return false;
    }
  }

  // ----------------------------------------------------------------- sessions

  Future<List<Session>> sessions() async =>
      (await _getJsonList('/api/session'))
          .whereType<Map<String, dynamic>>()
          .map(Session.fromJson)
          .toList();

  Future<Session> session(String id) async =>
      Session.fromJson(await _getJson('/api/session/$id'));

  Future<Session> createSession({
    required String directory,
    String? title,
  }) async => Session.fromJson(
    await _send('POST', '/api/session', {
      'directory': directory,
      if (title != null && title.isNotEmpty) 'title': title,
    }),
  );

  Future<void> deleteSession(String id) async =>
      _send('DELETE', '/api/session/$id');

  Future<void> renameSession(String id, String title) async =>
      _send('POST', '/api/session/$id/rename', {'title': title});

  Future<void> forkSession(String id) async =>
      _send('POST', '/api/session/$id/fork');

  Future<void> compactSession(String id) async =>
      _send('POST', '/api/session/$id/compact');

  Future<List<Session>> children(String id) async =>
      (await _getJsonList('/api/session/$id/children'))
          .whereType<Map<String, dynamic>>()
          .map(Session.fromJson)
          .toList();

  Future<List<Message>> messages(String id) async =>
      (await _getJsonList('/api/session/$id/message'))
          .whereType<Map<String, dynamic>>()
          .map(Message.fromJson)
          .toList();

  /// Sends input; returns as soon as the prompt is durably admitted.
  Future<String> prompt(
    String id,
    String text, {
    String delivery = 'queue',
    List<FileAttachment> files = const [],
  }) async {
    final out = await _send('POST', '/api/session/$id/prompt', {
      'text': text,
      'delivery': delivery,
      if (files.isNotEmpty) 'files': files.map((f) => f.toJson()).toList(),
    });
    return out['messageID'] as String? ?? '';
  }

  Future<void> interrupt(String id) async =>
      _send('POST', '/api/session/$id/interrupt');

  Future<void> setModel(
    String id,
    String providerID,
    String modelID, {
    String? variant,
  }) async => _send('POST', '/api/session/$id/model', {
    'providerID': providerID,
    'id': modelID,
    if (variant != null && variant.isNotEmpty) 'variant': variant,
  });

  Future<void> setAgent(String id, String agent) async =>
      _send('POST', '/api/session/$id/agent', {'agent': agent});

  Future<List<Todo>> todos(String id) async =>
      (await _getJsonList('/api/session/$id/todo'))
          .whereType<Map<String, dynamic>>()
          .map(Todo.fromJson)
          .toList();

  Future<SessionStats> stats(String id) async =>
      SessionStats.fromJson(await _getJson('/api/session/$id/stats'));

  /// Busy flag — is a turn running right now. The authoritative answer the
  /// run.started/ended events only announce.
  Future<bool> busy(String id) async =>
      (await _getJson('/api/session/$id/status'))['busy'] as bool? ?? false;

  Future<List<QueuedPrompt>> queue(String id) async =>
      (await _getJsonList('/api/session/$id/queue'))
          .whereType<Map<String, dynamic>>()
          .map(QueuedPrompt.fromJson)
          .toList();

  // ------------------------------------------------------------- permissions

  Future<List<PermissionRequest>> permissionRequests() async =>
      (await _getJsonList('/api/permission/request'))
          .whereType<Map<String, dynamic>>()
          .map(PermissionRequest.fromJson)
          .toList();

  Future<List<PermissionRequest>> sessionPermissions(String id) async =>
      (await _getJsonList('/api/session/$id/permission'))
          .whereType<Map<String, dynamic>>()
          .map(PermissionRequest.fromJson)
          .toList();

  /// [reply] is one of "once", "always", "reject".
  Future<void> replyPermission(
    String sessionID,
    String requestID,
    String reply, {
    String? message,
  }) async =>
      _send('POST', '/api/session/$sessionID/permission/$requestID/reply', {
        'reply': reply,
        if (message != null && message.isNotEmpty) 'message': message,
      });

  // -------------------------------------------------------------- questions

  Future<List<QuestionRequest>> questions() async =>
      (await _getJsonList('/api/question'))
          .whereType<Map<String, dynamic>>()
          .map(QuestionRequest.fromJson)
          .toList();

  Future<List<QuestionRequest>> sessionQuestions(String id) async =>
      (await _getJsonList('/api/session/$id/question'))
          .whereType<Map<String, dynamic>>()
          .map(QuestionRequest.fromJson)
          .toList();

  /// One entry per question in the request, each holding chosen labels.
  Future<void> replyQuestion(
    String requestID,
    List<List<String>> answers,
  ) async =>
      _send('POST', '/api/question/$requestID/reply', {'answers': answers});

  Future<void> rejectQuestion(String requestID) async =>
      _send('POST', '/api/question/$requestID/reject');

  // ------------------------------------------------------ providers & models

  Future<List<ModelEntry>> models() async =>
      (await _getJsonList('/api/model'))
          .whereType<Map<String, dynamic>>()
          .map(ModelEntry.fromJson)
          .toList();

  Future<List<Provider>> providers({bool all = false}) async =>
      (await _getJsonList('/api/provider${all ? '?all=true' : ''}'))
          .whereType<Map<String, dynamic>>()
          .map(Provider.fromJson)
          .toList();

  Future<List<AuthMethod>> authMethods(String providerID) async =>
      (await _getJsonList('/api/provider/$providerID/auth'))
          .whereType<Map<String, dynamic>>()
          .map(AuthMethod.fromJson)
          .toList();

  Future<void> setProviderKey(String providerID, String key) async =>
      _send('POST', '/api/provider/$providerID/auth', {'key': key});

  Future<void> logoutProvider(String providerID) async =>
      _send('DELETE', '/api/provider/$providerID/auth');

  Future<OAuthAttempt> startOAuth(
    String providerID,
    String method, [
    Map<String, String> answers = const {},
  ]) async => OAuthAttempt.fromJson(
    await _send('POST', '/api/provider/$providerID/auth/oauth', {
      'method': method,
      'answers': answers,
    }),
  );

  Future<OAuthAttempt> oauthStatus(String attemptID) async =>
      OAuthAttempt.fromJson(
        await _getJson('/api/provider/auth/oauth/$attemptID'),
      );

  // ---------------------------------------------------------------- account

  /// The gocoder.org account behind the server's sign-in.
  Future<AccountInfo> account() async =>
      AccountInfo.fromJson(await _getJson('/api/account'));

  Future<AccountInfo> signIn(String email, String password) async =>
      AccountInfo.fromJson(
        await _send('POST', '/api/account/login', {
          'email': email,
          'password': password,
        }),
      );

  Future<AccountInfo> signOut() async =>
      AccountInfo.fromJson(await _send('POST', '/api/account/logout'));

  Future<AccountInfo> renameAccount(String displayName) async =>
      AccountInfo.fromJson(
        await _send('PATCH', '/api/account', {'displayName': displayName}),
      );

  Future<UsageSummary> accountUsage({int days = 30}) async =>
      UsageSummary.fromJson(await _getJson('/api/account/usage?days=$days'));

  Future<InviteInfo> accountInvite() async =>
      InviteInfo.fromJson(await _getJson('/api/account/invite'));

  // --------------------------------------------------------------- catalog

  Future<List<Agent>> agents() async =>
      (await _getJsonList('/api/agent'))
          .whereType<Map<String, dynamic>>()
          .map(Agent.fromJson)
          .toList();

  Future<List<Command>> commands() async =>
      (await _getJsonList('/api/command'))
          .whereType<Map<String, dynamic>>()
          .map(Command.fromJson)
          .toList();

  Future<List<Skill>> skills() async =>
      (await _getJsonList('/api/skill'))
          .whereType<Map<String, dynamic>>()
          .map(Skill.fromJson)
          .toList();

  Future<List<Memory>> memories() async =>
      (await _getJsonList('/api/memory'))
          .whereType<Map<String, dynamic>>()
          .map(Memory.fromJson)
          .toList();

  Future<List<McpServer>> mcpServers() async =>
      (await _getJsonList('/api/mcp'))
          .whereType<Map<String, dynamic>>()
          .map(McpServer.fromJson)
          .toList();

  Future<LspState> lsp() async => LspState.fromJson(await _getJson('/api/lsp'));

  // --------------------------------------------------------------------- vcs

  Future<VcsInfo> vcs() async => VcsInfo.fromJson(await _getJson('/api/vcs'));

  Future<List<FileDiff>> vcsDiff({String mode = 'git', int context = 0}) async {
    var path = '/api/vcs/diff?mode=${Uri.encodeQueryComponent(mode)}';
    if (context > 0) path += '&context=$context';
    return (await _getJsonList(path))
        .whereType<Map<String, dynamic>>()
        .map(FileDiff.fromJson)
        .toList();
  }

  void close() => _http.close();
}
