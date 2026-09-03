package modelsdev

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
	"sync"
)

// snapshotGz is the models.dev catalog captured at build time, the Go
// analogue of the GOCODE_MODELS_DEV bundler define that
// packages/opencode/script/build.ts injects into the TypeScript build.
//
// It is stored gzipped because the catalog is ~4.2MB of JSON that compresses
// to ~430KB, and it is only ever needed on the path where both the disk cache
// and the network have already missed — so paying decompression there is far
// cheaper than carrying 4MB of uncompressed text in every binary.
//
// Regenerate with script/generate-catalog.sh.
//
//go:generate ../script/generate-catalog.sh
//go:embed catalog.json.gz
var snapshotGz []byte

var (
	snapshotOnce sync.Once
	snapshotData Catalog
	snapshotErr  error
)

// Snapshot returns the build-time catalog compiled into the binary. The
// result is decoded once and shared; callers must not mutate it.
func Snapshot() (Catalog, error) {
	snapshotOnce.Do(func() {
		reader, err := gzip.NewReader(bytes.NewReader(snapshotGz))
		if err != nil {
			snapshotErr = fmt.Errorf("modelsdev: snapshot: %w", err)
			return
		}
		defer reader.Close()
		text, err := io.ReadAll(reader)
		if err != nil {
			snapshotErr = fmt.Errorf("modelsdev: snapshot: %w", err)
			return
		}
		snapshotData, snapshotErr = decode(string(text))
	})
	return snapshotData, snapshotErr
}
