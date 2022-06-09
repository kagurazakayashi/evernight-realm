// 驗證 Dart（Flutter Web 執行環境）解析 UUIDv7 與大整數字串（探針原型）。
//
// 用法：dart run check.dart <樣例.json 路徑>
// 以 BigInt 解析金額字串並與期望值比對；以正規式校驗 UUIDv7 格式。
// 另示範 double 解析大整數會失真，證明「字串協議」的必要性。
import 'dart:convert';
import 'dart:io';

void main(List<String> args) {
  final path = args.isEmpty ? 'sample.json' : args[0];
  final data = jsonDecode(File(path).readAsStringSync()) as Map<String, dynamic>;
  var failures = 0;

  // 期望值與 Go 側 gen.go 的邊界集合一致。
  final expected = <String, BigInt>{
    'max_int64': BigInt.parse('9223372036854775807'),
    'min_int64': BigInt.parse('-9223372036854775808'),
    'js_safe_max': BigInt.parse('9007199254740991'),
    'js_safe_max_plus_1': BigInt.parse('9007199254740992'),
    'hundred_k': BigInt.from(100000),
    'zero': BigInt.zero,
  };

  for (final e in data['amounts'] as List) {
    final name = e['name'] as String;
    final value = e['value'] as String;
    final parsed = BigInt.parse(value);
    final ok = parsed == expected[name];
    if (!ok) {
      failures++;
    }
    stdout.writeln(
        '[${ok ? "PASS" : "FAIL"}] Dart 金額 $name — parsed=$parsed expected=${expected[name]}');
  }

  // UUIDv7：version=7（第 13 個 hex 字元），variant=10 位元（第 17 個 hex 為 8/9/a/b）。
  final uuidRe =
      RegExp(r'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$');
  for (final e in data['ids'] as List) {
    final u = e['uuid7'] as String;
    final ok = uuidRe.hasMatch(u);
    if (!ok) {
      failures++;
    }
    stdout.writeln('[${ok ? "PASS" : "FAIL"}] Dart UUIDv7 ${e['name']} — $u');
  }

  // 失真示範：double 無法精確承載 int64 全範圍。
  final f = double.parse('9223372036854775807');
  stdout.writeln('[INFO] Dart double 失真示範 — 9223372036854775807 → $f');

  exit(failures == 0 ? 0 : 1);
}
