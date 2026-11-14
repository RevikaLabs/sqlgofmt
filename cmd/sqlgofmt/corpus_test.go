package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ajitpratap0/GoSQLX/pkg/sql/keywords"

	"github.com/RevikaLabs/sqlgofmt/internal/processor"
)

// TestCorpusRoundTrip walks testdata/corpus — Go files whose SQL literals
// are sqlgofmt's own canonical output, reviewed before being committed.
// Every fixture must come back byte-identical: a change that alters output
// for any covered construct trips a fixture immediately. This is an
// idempotency/regression guard, not an independent verifier of the style
// spec (the fixtures were produced by the formatter itself, like
// dimitri/sqlfmt's corpus).
func TestCorpusRoundTrip(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "corpus")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("corpus is empty")
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		out, changed, err := processor.Process(path, src, processor.Options{
			Dialect: keywords.DialectPostgreSQL,
		})
		if err != nil {
			t.Fatalf("%s: process error: %v", e.Name(), err)
		}
		if changed {
			t.Errorf("%s: not a fixed point (run: go run ./cmd/sqlgofmt -w testdata/corpus/*.go)\n--- diff head ---\n%.400s", e.Name(), out)
		}
		if string(out) != string(src) {
			t.Errorf("%s: output differs from input", e.Name())
		}
		n++
	}
	if n < 10 {
		t.Fatalf("expected at least 10 corpus files, got %d", n)
	}
}
