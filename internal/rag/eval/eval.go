// Package eval turns "does chunking work" and "does semantic search find
// the right thing" from spot-checked impressions into two numbers with
// error bars:
//
//   - Retrieval quality: MineGoldSet mines (query, relevant-region) pairs
//     from a repo's own commit history — a commit's subject line stands in
//     for a query a developer might type, the lines it touched stand in for
//     the answer — and Evaluate scores rag.Searcher against them with
//     standard IR metrics (Recall@K, MRR), each reported with a bootstrap
//     95% confidence interval. That interval is the point: with a few
//     hundred mined pairs it is what tells you whether a chunking or
//     embedding-model change actually moved the needle, versus noise from
//     which queries happened to be easy or hard.
//   - Chunk boundary integrity: EvaluateChunking compares the chunks
//     chunk.Walk produces against a file's real function/class/method
//     boundaries (from an LSP resolver — the same ground truth chunk.Walk's
//     own syntax-aware mode is built on) and reports what fraction of
//     symbols land inside a single chunk, making the trade-off
//     chunk.go's package doc otherwise only asserts qualitatively ("at the
//     cost of occasionally cutting a chunk mid-function") into a number.
//
// Both pipelines are read-only and additive: they exercise the existing
// rag.Searcher/chunk.Walk exactly as production code does, and change
// nothing they touch.
package eval
