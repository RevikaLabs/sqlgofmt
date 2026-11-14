package processor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// corpusLiterals extracts the SQL raw string literals of testdata/corpus
// as fuzz seeds: every corpus construct feeds the fuzzer so it starts from
// realistic queries instead of garbage.
func corpusLiterals(tb testing.TB) []string {
	tb.Helper()
	dir := filepath.Join("..", "..", "testdata", "corpus")
	entries, err := os.ReadDir(dir)
	if err != nil {
		tb.Fatalf("read corpus dir: %v", err)
	}
	re := regexp.MustCompile("(?s)`([^`]*)`")
	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			tb.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			if strings.TrimSpace(m[1]) != "" {
				out = append(out, m[1])
			}
		}
	}
	return out
}

func FuzzLayout(f *testing.F) {
	for _, s := range corpusLiterals(f) {
		f.Add(s)
	}
	// A few edge-shaped seeds the corpus does not cover.
	f.Add("SELECT 1")
	f.Add("select  'a'  ||  'b'   from   t")
	f.Add("SELECT 1 /* /* not nested */ */ FROM t")
	f.Add("SELECT $x$ $$ inner $$ $x$ FROM t")
	f.Add("WITH a AS (SELECT 1) SELECT * FROM a")
	// Fuzz-found regression: glued args frame whose padding normalization
	// differed between passes (non-idempotent).
	f.Add("SEt( 0\n  ())")
	f.Add("SEt( 0\n  (()))")
	// Fuzz-found regression: a trailing comment reclassified as leading
	// when its line broke made pass 2 take a different branch in clFrom.
	f.Add("SET FROM/*000000000000000000000000000000000000000000000000000000000000000000000*/''")
	// Fuzz-found regression: an empty item after a comma panicked the
	// fill branch (index out of range on &item[0]).
	f.Add("SET(;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;, )")

	f.Fuzz(func(t *testing.T, sql string) {
		var r SkipReason
		out, ok := LayoutWithReason(sql, "", &r)
		if !ok {
			// Skipped literals must never produce partial output.
			if out != "" {
				t.Fatalf("skip reason %d (%s) but output non-empty: %q", r, SkipString(r), out)
			}
			return
		}
		// Property 1: the output must be a fixed point.
		out2, ok2 := LayoutWithReason(out, "", &r)
		if !ok2 {
			t.Fatalf("layout rejected its own output (reason %d: %s)\ninput: %q\noutput: %q", r, SkipString(r), sql, out)
		}
		if out2 != out {
			t.Fatalf("not idempotent\ninput: %q\nfirst: %q\nsecond: %q", sql, out, out2)
		}
		// Property 2: the token stream is never changed — same atoms,
		// same text (keywords case-insensitive), same order.
		if !sameTokenStreams(rawAtoms(sql), rawAtoms(out)) {
			t.Fatalf("token stream changed\ninput: %q\noutput: %q", sql, out)
		}
	})
}
