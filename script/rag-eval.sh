#!/usr/bin/env bash
#
# Runs rag-plugin's two eval modes (chunk boundary integrity and retrieval
# Recall@K/MRR — see internal/rag/eval and `rag-plugin eval -h`), saves a
# timestamped JSON snapshot of each under reports/rag-eval/, and diffs the
# new run against the most recent previous one. That diff is the point:
# a single run's numbers only say where things stand, the diff says whether
# a change to chunkLines/chunkOverlap/the embedding model actually moved
# anything, or is noise at this sample size.
#
# Usage:
#   script/rag-eval.sh                         # evaluate this repo itself
#   script/rag-eval.sh -root /path/to/project   # evaluate another project
#   script/rag-eval.sh -- -k 20 -max-commits 500  # pass flags through to
#                                                  # `rag-plugin eval retrieval`
#
# Env:
#   RAG_EVAL_SKIP_RETRIEVAL=1   only run the chunking eval (no index/provider
#                               needed) — set this if the target project
#                               isn't indexed or no embeddings provider is
#                               configured.
#   RAG_EVAL_REPORTS_DIR        where snapshots land (default: reports/rag-eval
#                               under the repo root).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
eval_root="$repo_root"
retrieval_args=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    -root)
      eval_root="$(cd "$2" && pwd)"
      shift 2
      ;;
    --)
      shift
      retrieval_args=("$@")
      break
      ;;
    *)
      echo "usage: $0 [-root DIR] [-- <rag-plugin eval retrieval flags>]" >&2
      exit 1
      ;;
  esac
done

bin="$repo_root/cmd/rag-plugin/rag-plugin"
echo "building rag-plugin..."
( cd "$repo_root" && go build -o "$bin" ./cmd/rag-plugin )

reports_dir="${RAG_EVAL_REPORTS_DIR:-$repo_root/reports/rag-eval}"
mkdir -p "$reports_dir"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"

find_previous() {
  local pattern="$1" current="$2"
  find "$reports_dir" -maxdepth 1 -name "$pattern" ! -name "$(basename "$current")" 2>/dev/null | sort | tail -n1
}

echo "evaluating chunking ($eval_root)..."
chunks_out="$reports_dir/$timestamp-chunks.json"
prev_chunks="$(find_previous '*-chunks.json' "$chunks_out")"
"$bin" eval chunks -root "$eval_root" -json > "$chunks_out"

retrieval_out=""
prev_retrieval=""
if [[ "${RAG_EVAL_SKIP_RETRIEVAL:-0}" == "1" ]]; then
  echo "skipping retrieval eval (RAG_EVAL_SKIP_RETRIEVAL=1)"
else
  echo "evaluating retrieval ($eval_root)..."
  retrieval_out="$reports_dir/$timestamp-retrieval.json"
  prev_retrieval="$(find_previous '*-retrieval.json' "$retrieval_out")"
  if ! "$bin" eval retrieval -root "$eval_root" -json "${retrieval_args[@]}" > "$retrieval_out"; then
    echo "warning: retrieval eval failed (is the project indexed, and is an embeddings provider configured?) — showing chunking results only" >&2
    rm -f "$retrieval_out"
    retrieval_out=""
  fi
fi

echo
echo "=== rag-eval report: $timestamp ==="
python3 - "$prev_chunks" "$chunks_out" "$prev_retrieval" "$retrieval_out" <<'PY'
import json, sys

def load(path):
    if not path:
        return None
    with open(path) as f:
        return json.load(f)

def percentile(values, p):
    if not values:
        return 0.0
    vs = sorted(values)
    if len(vs) == 1:
        return vs[0]
    idx = p * (len(vs) - 1)
    lo = int(idx)
    hi = lo if idx == lo else lo + 1
    frac = idx - lo
    return vs[lo] * (1 - frac) + vs[hi] * frac

def integrity(report):
    symbols = report.get("Symbols") or 0
    return (report.get("FullyContained", 0) / symbols) if symbols else 0.0

def fmt(v):
    return f"{v:.3f}"

def chunk_line(name, before, after):
    a_int, a_p10 = integrity(after), percentile(after.get("ContainmentRatios"), 0.1)
    line = f"  {name:14s} integrity={fmt(a_int)}  p10={fmt(a_p10)}  ({after.get('Symbols', 0)} symbols)"
    if before:
        b_int = integrity(before)
        delta = a_int - b_int
        sign = "+" if delta > 0 else ""
        line += f"   was {fmt(b_int)} ({sign}{fmt(delta)})"
    print(line)

def ci_overlaps(a, b):
    return not (a["CILow"] > b["CIHigh"] or a["CIHigh"] < b["CILow"])

def stat_line(name, before, after):
    line = f"  {name:10s} {fmt(after['Mean'])}  [{fmt(after['CILow'])}, {fmt(after['CIHigh'])}] 95% CI"
    if before:
        verdict = "overlaps previous CI — treat as noise" if ci_overlaps(before, after) else "does NOT overlap previous CI — real change"
        line += f"\n             was {fmt(before['Mean'])}  [{fmt(before['CILow'])}, {fmt(before['CIHigh'])}] — {verdict}"
    print(line)

chunks_prev_path, chunks_new_path, retrieval_prev_path, retrieval_new_path = sys.argv[1:5]
chunks_prev = load(chunks_prev_path)
chunks_new = load(chunks_new_path)

print("chunk boundary integrity:")
for key, label in (("slidingWindow", "sliding window"), ("syntaxAware", "syntax-aware")):
    chunk_line(label, (chunks_prev or {}).get(key), chunks_new[key])

retrieval_new = load(retrieval_new_path)
print()
print("retrieval:")
if retrieval_new is None:
    print("  skipped")
else:
    retrieval_prev = load(retrieval_prev_path)
    print(f"  gold pairs: {retrieval_new['N']}  (k={retrieval_new['K']})")
    stat_line("Recall@K", (retrieval_prev or {}).get("RecallAtK"), retrieval_new["RecallAtK"])
    stat_line("MRR", (retrieval_prev or {}).get("MRR"), retrieval_new["MRR"])
PY

echo
echo "snapshots written to $reports_dir"
