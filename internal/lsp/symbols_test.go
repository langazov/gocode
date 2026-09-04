package lsp

import (
	"context"
	"testing"
)

// TestServiceDocumentSymbols drives Service.DocumentSymbols end to end
// through the fake server (real spawn, real Content-Length framing), using
// its FUNC: marker convention (see fakeserver_test.go) to build a known
// symbol tree.
func TestServiceDocumentSymbols(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "main.fake", "package main\n\nFUNC:One\nbody\nbody\n\nFUNC:Two\nbody\n")

	service := newFakeService(t, dir, []string{".fake"})
	symbols, err := service.DocumentSymbols(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 2 {
		t.Fatalf("got %d symbols, want 2: %+v", len(symbols), symbols)
	}
	if symbols[0].Name != "One" || symbols[0].Range.Start.Line != 2 {
		t.Errorf("symbol 0: got %+v", symbols[0])
	}
	if symbols[1].Name != "Two" || symbols[1].Range.Start.Line != 6 {
		t.Errorf("symbol 1: got %+v", symbols[1])
	}
}

// TestServiceDocumentSymbolsNoServer: a file whose extension no server
// handles returns (nil, nil), not an error — the caller (the RAG plugin's
// chunker) falls back to its sliding-window split on this, not on an error
// path.
func TestServiceDocumentSymbolsNoServer(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "main.unhandled", "content\n")

	service := newFakeService(t, dir, []string{".fake"})
	symbols, err := service.DocumentSymbols(context.Background(), file)
	if err != nil {
		t.Fatalf("got error %v, want nil", err)
	}
	if symbols != nil {
		t.Errorf("got %+v, want nil", symbols)
	}
}

// TestNilServiceDocumentSymbols: the package-wide "a nil *Service does
// nothing" contract extends to DocumentSymbols too.
func TestNilServiceDocumentSymbols(t *testing.T) {
	var service *Service
	symbols, err := service.DocumentSymbols(context.Background(), "/does/not/matter")
	if err != nil || symbols != nil {
		t.Errorf("got (%v, %v), want (nil, nil)", symbols, err)
	}
}

// TestDecodeDocumentSymbolsFlatShape covers the older SymbolInformation[]
// response shape (location-wrapped range) some servers still send instead of
// the hierarchical DocumentSymbol[] shape.
func TestDecodeDocumentSymbolsFlatShape(t *testing.T) {
	raw := []byte(`[{"name":"Foo","kind":12,"location":{"uri":"file:///x","range":{"start":{"line":1,"character":0},"end":{"line":3,"character":0}}}}]`)
	symbols, err := decodeDocumentSymbols(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 1 || symbols[0].Name != "Foo" || symbols[0].Range.Start.Line != 1 || symbols[0].Range.End.Line != 3 {
		t.Errorf("got %+v", symbols)
	}
}

func TestDecodeDocumentSymbolsEmptyOrNull(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("null"), []byte("[]")} {
		symbols, err := decodeDocumentSymbols(raw)
		if err != nil || symbols != nil {
			t.Errorf("raw=%q: got (%v, %v), want (nil, nil)", raw, symbols, err)
		}
	}
}
