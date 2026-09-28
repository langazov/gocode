import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:re_highlight/languages/bash.dart';
import 'package:re_highlight/languages/c.dart';
import 'package:re_highlight/languages/cpp.dart';
import 'package:re_highlight/languages/css.dart';
import 'package:re_highlight/languages/dart.dart';
import 'package:re_highlight/languages/diff.dart';
import 'package:re_highlight/languages/dockerfile.dart';
import 'package:re_highlight/languages/go.dart';
import 'package:re_highlight/languages/ini.dart';
import 'package:re_highlight/languages/java.dart';
import 'package:re_highlight/languages/javascript.dart';
import 'package:re_highlight/languages/json.dart';
import 'package:re_highlight/languages/kotlin.dart';
import 'package:re_highlight/languages/makefile.dart';
import 'package:re_highlight/languages/markdown.dart';
import 'package:re_highlight/languages/php.dart';
import 'package:re_highlight/languages/protobuf.dart';
import 'package:re_highlight/languages/python.dart';
import 'package:re_highlight/languages/ruby.dart';
import 'package:re_highlight/languages/rust.dart';
import 'package:re_highlight/languages/sql.dart';
import 'package:re_highlight/languages/swift.dart';
import 'package:re_highlight/languages/typescript.dart';
import 'package:re_highlight/languages/xml.dart';
import 'package:re_highlight/languages/yaml.dart';
import 'package:re_highlight/re_highlight.dart';

import '../../app/theme.dart';

// ---------------------------------------------------------------------------
// Summaries

/// The one-line gist of a tool call for its collapsed row: the command, the
/// path, the pattern — whatever identifies the call — or null to fall back
/// to a generic preview.
String? toolSummary(String? name, Map<String, dynamic>? input) {
  if (input == null) return null;
  String? str(String key) {
    final v = input[key];
    return v is String && v.trim().isNotEmpty ? v.trim() : null;
  }

  final summary = switch (name) {
    'bash' => str('command'),
    'read' || 'write' || 'edit' => str('path'),
    'grep' || 'glob' => [
      ?str('pattern'),
      if (str('path') case final p?) 'in $p',
    ].join(' '),
    'webfetch' => str('url'),
    'websearch' => str('query'),
    'task' => str('description'),
    'skill' => str('name'),
    'todowrite' => switch (input['todos']) {
      final List<dynamic> todos => '${todos.length} todos',
      _ => null,
    },
    _ => null,
  };
  if (summary == null || summary.isEmpty) return null;
  // Rows are one line; a multi-line command reads as its first line.
  final firstLine = summary.split('\n').first;
  return firstLine.length < summary.length ? '$firstLine …' : firstLine;
}

// ---------------------------------------------------------------------------
// Details

/// A tool call's input and output, laid out for the tool that ran: bash as
/// a highlighted command and its terminal output, file tools with their
/// path and highlighted content, edits as diffs, todos as a checklist.
/// Tools it doesn't know (MCP, plugins) get a readable key/value list.
class ToolDetails extends StatelessWidget {
  const ToolDetails({
    super.key,
    required this.name,
    required this.input,
    required this.output,
  });

  final String? name;
  final Map<String, dynamic> input;
  final String? output;

  String? _str(String key) {
    final v = input[key];
    return v is String && v.isNotEmpty ? v : null;
  }

  num? _num(String key) {
    final v = input[key];
    return v is num ? v : null;
  }

  @override
  Widget build(BuildContext context) {
    final out = output == null || output!.isEmpty ? null : output!;
    final children = switch (name) {
      'bash' => _bash(out),
      'read' => _read(out),
      'write' => _write(out),
      'edit' => _edit(out),
      'apply_patch' => _patch(out),
      'grep' || 'glob' => _search(out),
      'webfetch' => _fetch(out),
      'websearch' => _websearch(out),
      'task' => _task(out),
      'todowrite' => _todos(out),
      _ => _generic(out),
    };
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        for (final (i, child) in children.indexed) ...[
          if (i > 0) const SizedBox(height: 8),
          child,
        ],
      ],
    );
  }

  List<Widget> _bash(String? out) => [
    if (_str('command') case final command?)
      SyntaxCode(command, language: 'bash', prompt: r'$ '),
    ?_meta([
      if (_str('workdir') case final dir?) 'in $dir',
      if (_num('timeout') case final t?) 'timeout ${_duration(t)}',
    ]),
    if (out != null) SyntaxCode(out, maxHeight: 280),
  ];

  List<Widget> _read(String? out) {
    final path = _str('path');
    final offset = _num('offset');
    final limit = _num('limit');
    return [
      _PathLine(
        path: path,
        note: switch ((offset, limit)) {
          (null, null) => null,
          (final o?, final l?) => 'lines $o–${o + l - 1}',
          (final o?, null) => 'from line $o',
          (null, final l?) => 'first $l lines',
        },
      ),
      if (out != null) ..._numberedOrPlain(out, languageForPath(path)),
    ];
  }

  /// The read tool prints `N: text` per line; split that into a gutter and
  /// highlightable source. Anything else (a directory listing, a notice)
  /// renders as-is.
  List<Widget> _numberedOrPlain(String out, String? language) {
    final lines = out.split('\n');
    final numbers = <int>[];
    final source = <String>[];
    final pattern = RegExp(r'^(\d+): ?(.*)$');
    for (final line in lines) {
      final m = pattern.firstMatch(line);
      if (m == null) return [SyntaxCode(out, maxHeight: 320)];
      numbers.add(int.parse(m.group(1)!));
      source.add(m.group(2)!);
    }
    return [
      SyntaxCode(
        source.join('\n'),
        language: language,
        lineNumbers: numbers,
        maxHeight: 320,
      ),
    ];
  }

  List<Widget> _write(String? out) {
    final path = _str('path');
    final content = _str('content') ?? '';
    return [
      _PathLine(path: path, note: '${_lineCount(content)} lines'),
      if (content.isNotEmpty)
        SyntaxCode(
          content,
          language: languageForPath(path),
          lineNumbers: [for (var i = 1; i <= _lineCount(content); i++) i],
          maxHeight: 320,
        ),
      if (out != null) _Note(out),
    ];
  }

  List<Widget> _edit(String? out) {
    final path = _str('path');
    return [
      _PathLine(
        path: path,
        note: input['replaceAll'] == true ? 'all occurrences' : null,
      ),
      _DiffLines(lineDiff(_str('oldString') ?? '', _str('newString') ?? '')),
      if (out != null) _Note(out),
    ];
  }

  List<Widget> _patch(String? out) => [
    if (_str('patchText') case final text?) _DiffLines(classifyPatch(text)),
    if (out != null) _Note(out),
  ];

  List<Widget> _search(String? out) => [
    _QueryLine(
      query: _str('pattern') ?? '',
      notes: [
        if (_str('path') case final p?) 'in $p',
        if (_str('include') case final i?) 'include $i',
        if (_num('limit') case final l?) 'limit $l',
      ],
    ),
    if (out != null) SyntaxCode(out, maxHeight: 280),
  ];

  List<Widget> _fetch(String? out) => [
    _QueryLine(query: _str('url') ?? '', link: true, notes: [?_str('format')]),
    if (out != null)
      SyntaxCode(
        out,
        language: _str('format') == 'markdown' ? 'markdown' : null,
        maxHeight: 280,
      ),
  ];

  List<Widget> _websearch(String? out) => [
    _QueryLine(
      query: _str('query') ?? '',
      notes: [if (_num('numResults') case final n?) '$n results'],
    ),
    if (out != null) SyntaxCode(out, maxHeight: 280),
  ];

  List<Widget> _task(String? out) => [
    _QueryLine(
      query: _str('description') ?? 'task',
      plain: true,
      notes: [?_str('subagent_type')],
    ),
    if (_str('prompt') case final prompt?) _Prose(prompt),
    if (out != null) _Prose(out),
  ];

  List<Widget> _todos(String? out) {
    final todos = input['todos'];
    if (todos is! List) return _generic(out);
    return [
      for (final todo in todos.whereType<Map<String, dynamic>>())
        _TodoLine(
          content: todo['content']?.toString() ?? '',
          status: todo['status']?.toString() ?? 'pending',
        ),
    ];
  }

  List<Widget> _generic(String? out) => [
    if (input.isNotEmpty) _KeyValues(input),
    if (out != null) _output(out),
  ];

  /// Output of a tool we know nothing about: JSON gets pretty-printed and
  /// highlighted, anything else is shown as-is.
  Widget _output(String out) {
    final trimmed = out.trimLeft();
    if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
      try {
        final pretty = const JsonEncoder.withIndent('  ')
            .convert(jsonDecode(out));
        return SyntaxCode(pretty, language: 'json', maxHeight: 280);
      } on FormatException {
        // Not JSON after all.
      }
    }
    return SyntaxCode(out, maxHeight: 280);
  }

  static Widget? _meta(List<String> parts) =>
      parts.isEmpty ? null : _Note(parts.join('  ·  '));

  static int _lineCount(String s) =>
      s.isEmpty ? 0 : '\n'.allMatches(s.trimRight()).length + 1;

  /// Tool timeouts are milliseconds.
  static String _duration(num ms) => ms >= 1000
      ? '${(ms / 1000).toStringAsFixed(ms % 1000 == 0 ? 0 : 1)}s'
      : '${ms}ms';
}

// ---------------------------------------------------------------------------
// Pieces

class _PathLine extends StatelessWidget {
  const _PathLine({required this.path, this.note});

  final String? path;
  final String? note;

  @override
  Widget build(BuildContext context) {
    return Text.rich(
      TextSpan(
        children: [
          TextSpan(
            text: path ?? '(no path)',
            style: _mono.copyWith(color: GC.textHi),
          ),
          if (note != null) TextSpan(text: '   $note', style: _noteStyle),
        ],
      ),
    );
  }
}

/// A search-ish input: the query itself prominent, qualifiers faint.
class _QueryLine extends StatelessWidget {
  const _QueryLine({
    required this.query,
    this.notes = const [],
    this.link = false,
    this.plain = false,
  });

  final String query;
  final List<String> notes;
  final bool link;

  /// Prose (a task's description) rather than a pattern.
  final bool plain;

  @override
  Widget build(BuildContext context) {
    return SelectableText.rich(
      TextSpan(
        children: [
          TextSpan(
            text: query,
            style: plain
                ? const TextStyle(
                    fontFamily: GC.sans,
                    fontSize: 13,
                    fontWeight: FontWeight.w600,
                    color: GC.textHi,
                  )
                : _mono.copyWith(
                    color: link ? GC.accentText : GC.textHi,
                    decoration: link ? TextDecoration.underline : null,
                    decorationColor: GC.accentText.withValues(alpha: 0.4),
                  ),
          ),
          if (notes.isNotEmpty)
            TextSpan(text: '   ${notes.join('  ·  ')}', style: _noteStyle),
        ],
      ),
    );
  }
}

class _Note extends StatelessWidget {
  const _Note(this.text);

  final String text;

  @override
  Widget build(BuildContext context) => Text(text, style: _noteStyle);
}

class _Prose extends StatelessWidget {
  const _Prose(this.text);

  final String text;

  @override
  Widget build(BuildContext context) {
    return _Well(
      maxHeight: 240,
      child: SelectableText(
        text,
        style: const TextStyle(
          fontFamily: GC.sans,
          fontSize: 12.5,
          height: 1.5,
          color: GC.textBody,
        ),
      ),
    );
  }
}

class _TodoLine extends StatelessWidget {
  const _TodoLine({required this.content, required this.status});

  final String content;
  final String status;

  @override
  Widget build(BuildContext context) {
    final (icon, color) = switch (status) {
      'completed' => (Icons.check_circle_rounded, GC.ok),
      'in_progress' => (Icons.radio_button_checked_rounded, GC.accentText),
      'cancelled' => (Icons.cancel_outlined, GC.textFaint),
      _ => (Icons.radio_button_unchecked_rounded, GC.textFaint),
    };
    final done = status == 'completed' || status == 'cancelled';
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 1),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.only(top: 2),
            child: Icon(icon, size: 13, color: color),
          ),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              content,
              style: TextStyle(
                fontFamily: GC.sans,
                fontSize: 12.5,
                height: 1.4,
                color: done ? GC.textFaint : GC.textBody,
                decoration: status == 'cancelled'
                    ? TextDecoration.lineThrough
                    : null,
              ),
            ),
          ),
        ],
      ),
    );
  }
}

/// Unknown tools: one row per argument, short values inline, long or
/// structured ones in a well beneath their key.
class _KeyValues extends StatelessWidget {
  const _KeyValues(this.values);

  final Map<String, dynamic> values;

  @override
  Widget build(BuildContext context) {
    final rows = <Widget>[];
    for (final MapEntry(:key, :value) in values.entries) {
      final text = value is String
          ? value
          : const JsonEncoder.withIndent('  ').convert(value);
      final block = text.contains('\n') || text.length > 80;
      rows.add(
        Padding(
          padding: const EdgeInsets.symmetric(vertical: 2),
          child: block
              ? Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    Text(key, style: _noteStyle),
                    const SizedBox(height: 3),
                    SyntaxCode(
                      text,
                      language: value is String ? null : 'json',
                      maxHeight: 200,
                    ),
                  ],
                )
              : Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    ConstrainedBox(
                      constraints: const BoxConstraints(minWidth: 72),
                      child: Text(key, style: _noteStyle.copyWith(height: 1.5)),
                    ),
                    const SizedBox(width: 10),
                    Expanded(
                      child: SelectableText(
                        text,
                        style: _mono.copyWith(
                          color: value is String ? GC.textHi : _numberColor,
                        ),
                      ),
                    ),
                  ],
                ),
        ),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: rows,
    );
  }
}

// ---------------------------------------------------------------------------
// Diffs

enum DiffKind { context, add, remove, header }

/// A line diff of [before] → [after]: the shared leading and trailing lines
/// as context, the differing middle as removals then additions. Edits are
/// usually one contiguous change, where this reads the same as a real diff.
List<(DiffKind, String)> lineDiff(String before, String after) {
  final a = before.split('\n');
  final b = after.split('\n');
  var start = 0;
  while (start < a.length && start < b.length && a[start] == b[start]) {
    start++;
  }
  var endA = a.length;
  var endB = b.length;
  while (endA > start && endB > start && a[endA - 1] == b[endB - 1]) {
    endA--;
    endB--;
  }
  return [
    for (final l in a.sublist(0, start)) (DiffKind.context, l),
    for (final l in a.sublist(start, endA)) (DiffKind.remove, l),
    for (final l in b.sublist(start, endB)) (DiffKind.add, l),
    for (final l in a.sublist(endA)) (DiffKind.context, l),
  ];
}

/// Classifies apply_patch's `*** Begin Patch` format (and unified diffs)
/// line by line.
List<(DiffKind, String)> classifyPatch(String patch) => [
  for (final line in patch.trimRight().split('\n'))
    if (line.startsWith('***') ||
        line.startsWith('@@') ||
        line.startsWith('+++') ||
        line.startsWith('---') ||
        line.startsWith('diff '))
      (DiffKind.header, line)
    else if (line.startsWith('+'))
      (DiffKind.add, line.substring(1))
    else if (line.startsWith('-'))
      (DiffKind.remove, line.substring(1))
    else
      (DiffKind.context, line.startsWith(' ') ? line.substring(1) : line),
];

class _DiffLines extends StatelessWidget {
  const _DiffLines(this.lines);

  final List<(DiffKind, String)> lines;

  @override
  Widget build(BuildContext context) {
    return _Well(
      maxHeight: 360,
      padding: const EdgeInsets.symmetric(vertical: 6),
      // Washes span the full well even when every line is short, and the
      // widest line when one is longer than the well.
      child: LayoutBuilder(
        builder: (context, constraints) => SingleChildScrollView(
          scrollDirection: Axis.horizontal,
          child: ConstrainedBox(
            constraints: BoxConstraints(minWidth: constraints.maxWidth),
            child: IntrinsicWidth(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  for (final (kind, text) in lines)
                    Container(
                      color: switch (kind) {
                        DiffKind.add => GC.ok.withValues(alpha: 0.10),
                        DiffKind.remove => GC.down.withValues(alpha: 0.12),
                        _ => null,
                      },
                      padding: const EdgeInsets.only(left: 8, right: 12),
                      child: Text.rich(
                        TextSpan(
                          children: [
                            TextSpan(
                              text: switch (kind) {
                                DiffKind.add => '+ ',
                                DiffKind.remove => '- ',
                                DiffKind.header => '',
                                DiffKind.context => '  ',
                              },
                              style: _codeStyle.copyWith(
                                color: switch (kind) {
                                  DiffKind.add => GC.ok,
                                  DiffKind.remove => GC.downText,
                                  _ => GC.textFaint,
                                },
                              ),
                            ),
                            TextSpan(
                              text: text.isEmpty ? ' ' : _detab(text),
                              style: _codeStyle.copyWith(
                                color: switch (kind) {
                                  DiffKind.header => GC.accentText,
                                  DiffKind.context => GC.textDim,
                                  _ => GC.textHi,
                                },
                              ),
                            ),
                          ],
                        ),
                        softWrap: false,
                      ),
                    ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

// ---------------------------------------------------------------------------
// Highlighted code

final Highlight _highlight = Highlight()
  ..registerLanguages({
    'bash': langBash,
    'c': langC,
    'cpp': langCpp,
    'css': langCss,
    'dart': langDart,
    'diff': langDiff,
    'dockerfile': langDockerfile,
    'go': langGo,
    'ini': langIni,
    'java': langJava,
    'javascript': langJavascript,
    'json': langJson,
    'kotlin': langKotlin,
    'makefile': langMakefile,
    'markdown': langMarkdown,
    'php': langPhp,
    'protobuf': langProtobuf,
    'python': langPython,
    'ruby': langRuby,
    'rust': langRust,
    'sql': langSql,
    'swift': langSwift,
    'typescript': langTypescript,
    'xml': langXml,
    'yaml': langYaml,
  });

/// The highlighter language for a file path, by name or extension.
String? languageForPath(String? path) {
  if (path == null) return null;
  final name = path.split(RegExp(r'[/\\]')).last.toLowerCase();
  if (name == 'dockerfile') return 'dockerfile';
  if (name == 'makefile' || name == 'gnumakefile') return 'makefile';
  final dot = name.lastIndexOf('.');
  if (dot < 0) return null;
  return switch (name.substring(dot + 1)) {
    'sh' || 'bash' || 'zsh' => 'bash',
    'c' || 'h' => 'c',
    'cc' || 'cpp' || 'cxx' || 'hpp' || 'hh' => 'cpp',
    'css' || 'scss' => 'css',
    'dart' => 'dart',
    'diff' || 'patch' => 'diff',
    'go' => 'go',
    'ini' || 'toml' || 'cfg' || 'conf' => 'ini',
    'java' => 'java',
    'js' || 'mjs' || 'cjs' || 'jsx' => 'javascript',
    'json' || 'jsonc' => 'json',
    'kt' || 'kts' => 'kotlin',
    'md' || 'markdown' => 'markdown',
    'php' => 'php',
    'proto' => 'protobuf',
    'py' => 'python',
    'rb' => 'ruby',
    'rs' => 'rust',
    'sql' => 'sql',
    'swift' => 'swift',
    'ts' || 'tsx' || 'mts' => 'typescript',
    'html' || 'htm' || 'xml' || 'svg' || 'plist' => 'xml',
    'yaml' || 'yml' => 'yaml',
    _ => null,
  };
}

/// Beyond this, highlighting costs more than it's worth: shown plain.
const _highlightLimit = 60000;

/// Monospace code in a dark well, highlighted when [language] is known.
///
/// With [lineNumbers] (one per line) it gets a gutter and scrolls
/// horizontally instead of wrapping, so numbers stay aligned with lines.
class SyntaxCode extends StatefulWidget {
  const SyntaxCode(
    this.code, {
    super.key,
    this.language,
    this.lineNumbers,
    this.prompt,
    this.maxHeight = 240,
  });

  final String code;
  final String? language;
  final List<int>? lineNumbers;

  /// A faint prefix before the code, e.g. a shell's `$ `.
  final String? prompt;
  final double maxHeight;

  @override
  State<SyntaxCode> createState() => _SyntaxCodeState();
}

class _SyntaxCodeState extends State<SyntaxCode> {
  late TextSpan _span = _render();

  @override
  void didUpdateWidget(SyntaxCode oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.code != widget.code ||
        oldWidget.language != widget.language ||
        oldWidget.prompt != widget.prompt) {
      _span = _render();
    }
  }

  TextSpan _render() {
    final code = _detab(widget.code);
    final language = widget.language;
    TextSpan body = TextSpan(text: code);
    if (language != null &&
        code.length <= _highlightLimit &&
        _highlight.getLanguage(language) != null) {
      try {
        final renderer = TextSpanRenderer(null, _syntaxTheme);
        _highlight.highlight(code: code, language: language).render(renderer);
        body = renderer.span ?? body;
      } catch (_) {
        // A grammar hiccup must not take the timeline down; show it plain.
      }
    }
    return TextSpan(
      style: _codeStyle,
      children: [
        if (widget.prompt != null)
          TextSpan(
            text: widget.prompt,
            style: const TextStyle(color: GC.textFaint),
          ),
        body,
      ],
    );
  }

  @override
  Widget build(BuildContext context) {
    final numbers = widget.lineNumbers;
    if (numbers == null) {
      return _Well(
        maxHeight: widget.maxHeight,
        child: SelectableText.rich(_span),
      );
    }
    return _Well(
      maxHeight: widget.maxHeight,
      child: SingleChildScrollView(
        scrollDirection: Axis.horizontal,
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              numbers.join('\n'),
              textAlign: TextAlign.right,
              style: _codeStyle.copyWith(color: GC.textFaint),
            ),
            const SizedBox(width: 14),
            SelectableText.rich(_span),
          ],
        ),
      ),
    );
  }
}

/// The dark, rounded ground every code-ish block sits on.
class _Well extends StatelessWidget {
  const _Well({
    required this.child,
    required this.maxHeight,
    this.padding = const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
  });

  final Widget child;
  final double maxHeight;
  final EdgeInsets padding;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      constraints: BoxConstraints(maxHeight: maxHeight),
      decoration: BoxDecoration(
        color: const Color(0x40000000),
        borderRadius: BorderRadius.circular(6),
        border: Border.all(color: GC.border),
      ),
      child: SingleChildScrollView(padding: padding, child: child),
    );
  }
}

// ---------------------------------------------------------------------------
// Styles

const _codeStyle = TextStyle(
  fontFamily: GC.mono,
  fontSize: 12,
  height: 1.5,
  color: GC.textBody,
  // Show code as typed: JetBrains Mono's ligatures would draw `***` as two
  // glyphs and fuse `==`, `!=`, `->`.
  fontFeatures: [FontFeature.disable('liga'), FontFeature.disable('calt')],
);

/// Flutter renders a tab about one space wide; gofmt'd code needs more.
String _detab(String s) => s.replaceAll('\t', '    ');

final _mono = _codeStyle.copyWith(height: 1.4);

const _noteStyle = TextStyle(
  fontFamily: GC.sans,
  fontSize: 11.5,
  color: GC.textFaint,
);

const _numberColor = Color(0xFFE3C07E);

/// Syntax colours in the app's warm palette: accent keywords, sage strings,
/// gold numbers, dusty blue names, faint comments.
const _syntaxTheme = <String, TextStyle>{
  'keyword': TextStyle(color: GC.accentText),
  'meta-keyword': TextStyle(color: GC.accentText),
  'doctag': TextStyle(color: GC.accentText),
  'type': TextStyle(color: Color(0xFFE6B98A)),
  'built_in': TextStyle(color: Color(0xFFC9B3E6)),
  'title': TextStyle(color: GC.textHi),
  'title.function_': TextStyle(color: GC.textHi),
  'title.class_': TextStyle(color: Color(0xFFE6B98A)),
  'section': TextStyle(color: GC.accentText, fontWeight: FontWeight.w600),
  'string': TextStyle(color: Color(0xFFA9C98A)),
  'meta-string': TextStyle(color: Color(0xFFA9C98A)),
  'regexp': TextStyle(color: Color(0xFFA9C98A)),
  'subst': TextStyle(color: GC.textBody),
  'number': TextStyle(color: _numberColor),
  'literal': TextStyle(color: _numberColor),
  'symbol': TextStyle(color: _numberColor),
  'attr': TextStyle(color: Color(0xFF9FC2D8)),
  'attribute': TextStyle(color: Color(0xFF9FC2D8)),
  'variable': TextStyle(color: Color(0xFF9FC2D8)),
  'params': TextStyle(color: Color(0xFF9FC2D8)),
  'property': TextStyle(color: Color(0xFF9FC2D8)),
  'tag': TextStyle(color: GC.accentText),
  'name': TextStyle(color: GC.accentText),
  'selector-tag': TextStyle(color: GC.accentText),
  'meta': TextStyle(color: GC.textDim),
  'comment': TextStyle(color: GC.textFaint, fontStyle: FontStyle.italic),
  'quote': TextStyle(color: GC.textFaint, fontStyle: FontStyle.italic),
  'addition': TextStyle(color: GC.ok),
  'deletion': TextStyle(color: GC.downText),
  'bullet': TextStyle(color: GC.accentText),
  'emphasis': TextStyle(fontStyle: FontStyle.italic),
  'strong': TextStyle(fontWeight: FontWeight.w600),
  'link': TextStyle(color: GC.accentText),
  'code': TextStyle(color: Color(0xFFA9C98A)),
};
