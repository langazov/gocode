/// The gocoder.org account behind the server's sign-in (GET /api/account).
/// The server calls the site with its stored API key, so the app never holds
/// a gocoder.org credential itself.
class AccountInfo {
  const AccountInfo({
    required this.signedIn,
    required this.site,
    this.userID = '',
    this.email = '',
    this.displayName = '',
    this.keyPrefix = '',
    this.memberSince,
    this.expired = false,
    this.offline = false,
  });

  factory AccountInfo.fromJson(Map<String, dynamic> json) => AccountInfo(
    signedIn: json['signedIn'] as bool? ?? false,
    site: json['site'] as String? ?? '',
    userID: json['userId'] as String? ?? '',
    email: json['email'] as String? ?? '',
    displayName: json['displayName'] as String? ?? '',
    keyPrefix: json['keyPrefix'] as String? ?? '',
    memberSince: DateTime.tryParse(json['memberSince'] as String? ?? '')
        ?.toLocal(),
    expired: json['expired'] as bool? ?? false,
    offline: json['offline'] as bool? ?? false,
  );

  final bool signedIn;

  /// The gocoder.org instance, e.g. `https://gocoder.org`.
  final String site;
  final String userID;
  final String email;
  final String displayName;

  /// This machine's API key prefix; the key itself stays on the server.
  final String keyPrefix;
  final DateTime? memberSince;

  /// The site rejects the stored key (revoked, or the account was deleted).
  /// Signing in again fixes it.
  final bool expired;

  /// The site couldn't be reached; the details are the stored ones.
  final bool offline;

  /// The name to show: the display name, else the email's local part.
  String get name =>
      displayName.isNotEmpty ? displayName : email.split('@').first;

  /// Up to two initials for the avatar.
  String get initials {
    final words = name
        .trim()
        .split(RegExp(r'\s+'))
        .where((w) => w.isNotEmpty)
        .toList();
    if (words.isEmpty) return '?';
    String first(String word) =>
        String.fromCharCode(word.runes.first).toUpperCase();
    return words.length == 1
        ? first(words.first)
        : first(words.first) + first(words.last);
  }

  /// A page on the website: `page('/settings')` → `<site>/#/settings`.
  Uri page(String path) {
    final base = site.isEmpty
        ? 'https://gocoder.org'
        : site.replaceAll(RegExp(r'/+$'), '');
    return Uri.parse('$base/#$path');
  }
}

int _int(Object? value) => (value as num?)?.toInt() ?? 0;

/// Totals over one usage window.
class UsageTotals {
  const UsageTotals({
    this.requests = 0,
    this.tokens = 0,
    this.promptTokens = 0,
    this.cachedRead = 0,
    this.errors = 0,
    this.avgLatencyMs = 0,
  });

  factory UsageTotals.fromJson(Map<String, dynamic> json) => UsageTotals(
    requests: _int(json['requests']),
    tokens: _int(json['tokens']),
    promptTokens: _int(json['promptTokens']),
    cachedRead: _int(json['cachedRead']),
    errors: _int(json['errors']),
    avgLatencyMs: (json['avgLatencyMs'] as num?)?.toDouble() ?? 0,
  );

  final int requests;
  final int tokens;
  final int promptTokens;
  final int cachedRead;
  final int errors;
  final double avgLatencyMs;

  /// Share of prompt tokens served from cache; null when none were reported.
  double? get cacheHitRate =>
      promptTokens > 0 ? cachedRead / promptTokens : null;
}

/// One provider+model's share of a window.
class UsageModel {
  const UsageModel({
    required this.provider,
    required this.model,
    required this.requests,
    required this.tokens,
  });

  factory UsageModel.fromJson(Map<String, dynamic> json) => UsageModel(
    provider: json['provider'] as String? ?? '',
    model: json['model'] as String? ?? '',
    requests: _int(json['requests']),
    tokens: _int(json['tokens']),
  );

  final String provider;
  final String model;
  final int requests;
  final int tokens;
}

/// One zero-filled UTC day.
class UsageDay {
  const UsageDay({
    required this.date,
    required this.requests,
    required this.tokens,
  });

  factory UsageDay.fromJson(Map<String, dynamic> json) => UsageDay(
    date: DateTime.tryParse(json['date'] as String? ?? '') ?? DateTime(1970),
    requests: _int(json['requests']),
    tokens: _int(json['tokens']),
  );

  final DateTime date;
  final int requests;
  final int tokens;
}

/// The account's usage over a window of days (GET /api/account/usage).
class UsageSummary {
  const UsageSummary({
    required this.totals,
    required this.byModel,
    required this.daily,
  });

  factory UsageSummary.fromJson(Map<String, dynamic> json) => UsageSummary(
    totals: UsageTotals.fromJson(
      json['totals'] as Map<String, dynamic>? ?? const {},
    ),
    byModel: (json['byModel'] as List<dynamic>? ?? const [])
        .whereType<Map<String, dynamic>>()
        .map(UsageModel.fromJson)
        .toList(),
    daily: (json['daily'] as List<dynamic>? ?? const [])
        .whereType<Map<String, dynamic>>()
        .map(UsageDay.fromJson)
        .toList(),
  );

  final UsageTotals totals;
  final List<UsageModel> byModel;
  final List<UsageDay> daily;
}

/// The account's invite link and how many people joined through it.
class InviteInfo {
  const InviteInfo({
    required this.code,
    required this.url,
    required this.invited,
  });

  factory InviteInfo.fromJson(Map<String, dynamic> json) => InviteInfo(
    code: json['code'] as String? ?? '',
    url: json['url'] as String? ?? '',
    invited: _int(json['invited']),
  );

  final String code;
  final String url;
  final int invited;
}
