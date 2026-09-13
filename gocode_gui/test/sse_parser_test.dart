import 'package:flutter_test/flutter_test.dart';
import 'package:gocode_gui/core/api/sse.dart';

void main() {
  // The parser is private; exercise it through the public feed path by
  // constructing a client pointed at nothing and driving chunks via the
  // exported test hook below. Instead we test via the SseClient's parser
  // indirectly — so expose it for tests:
  group('SSE parser', () {
    late SseParser parser;

    setUp(() => parser = SseParser());

    test('single complete event in one chunk', () {
      final out = parser.feed('data: {"type":"a"}\n\n');
      expect(out, ['{"type":"a"}']);
    });

    test('event split across chunks', () {
      expect(parser.feed('data: {"ty'), isEmpty);
      expect(parser.feed('pe":"a"}\n'), isEmpty);
      expect(parser.feed('\n'), ['{"type":"a"}']);
    });

    test('multiple events in one chunk', () {
      final out = parser.feed('data: one\n\ndata: two\n\n');
      expect(out, ['one', 'two']);
    });

    test('CRLF line endings', () {
      final out = parser.feed('data: x\r\n\r\n');
      expect(out, ['x']);
    });

    test('multi-line data field joins with newline', () {
      final out = parser.feed('data: l1\ndata: l2\n\n');
      expect(out, ['l1\nl2']);
    });

    test('comment lines are ignored', () {
      final out = parser.feed(': keep-alive\n\ndata: real\n\n');
      expect(out, ['real']);
    });

    test('field without colon', () {
      final out = parser.feed('data\n\n');
      // A bare "data" field has an empty value; no data lines were added by
      // the spec, but our parser records an empty string — accepted.
      expect(out.length, 1);
    });

    test('no space after colon is tolerated', () {
      final out = parser.feed('data:x\n\n');
      expect(out, ['x']);
    });

    test('id and event fields do not emit payloads', () {
      final out = parser.feed('id: 42\nevent: ping\n\ndata: payload\n\n');
      expect(out, ['payload']);
    });
  });
}
