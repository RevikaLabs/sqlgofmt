//go:build pgquery

// Oracle test: the real PostgreSQL parser (libpg_query via pg_query_go)
// proves that formatting never changes a query's meaning. The in-band
// GoSQLX token-identity gate only proves GoSQLX's view of the text did not
// change; here we compare the PostgreSQL parse trees of input and output,
// with all source Location fields stripped (they legitimately shift when
// whitespace moves). Run with: go test -tags pgquery ./internal/processor/
package processor

import (
	"testing"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// stripLocations zeroes every Location-style field in the tree: whitespace
// changes are expected to move source offsets, nothing else.
func stripLocations(m protoreflect.Message) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() == protoreflect.MessageKind && fd.IsList() {
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				stripLocations(list.Get(i).Message())
			}
			return true
		}
		if fd.Kind() == protoreflect.MessageKind {
			stripLocations(v.Message())
			return true
		}
		switch string(fd.Name()) {
		case "location", "stmt_location", "stmt_len":
			m.Clear(fd)
		}
		return true
	})
}

// oracleParse parses SQL with the real PostgreSQL parser and returns the
// stripped protobuf tree.
func oracleParse(t *testing.T, sql string) protoreflect.Message {
	t.Helper()
	res, err := pg_query.Parse(sql)
	if err != nil {
		t.Fatalf("pg_query.Parse(%q): %v", sql, err)
	}
	msg := res.ProtoReflect()
	stripLocations(msg)
	return msg
}

// TestOracleCorpus: for every corpus literal, the PostgreSQL parse tree of
// the formatted output must equal the tree of the input. Format verbs are
// masked before parsing (the real parser rejects them) — identical masking
// on both sides keeps the comparison valid. Skipped literals have nothing
// to verify, but they must still parse (a corpus literal that fails
// pg_query.Parse is a corpus bug, not the tool's).
func TestOracleCorpus(t *testing.T) {
	for _, lit := range corpusLiterals(t) {
		var r SkipReason
		out, ok := LayoutWithReason(lit, "", &r)
		if !ok {
			if _, err := pg_query.Parse(maskSpans(lit, verbRe.FindAllStringIndex(lit, -1))); err != nil {
				t.Fatalf("corpus literal does not parse: %q: %v", lit, err)
			}
			continue
		}
		in := oracleParse(t, maskSpans(lit, verbRe.FindAllStringIndex(lit, -1)))
		got := oracleParse(t, maskSpans(out, verbRe.FindAllStringIndex(out, -1)))
		if !proto.Equal(in.Interface(), got.Interface()) {
			t.Fatalf("parse tree changed for %q\noutput: %q", lit, out)
		}
	}
}

// TestOracleTrickyLiterals pins the whitespace-sensitive cases the GoSQLX
// token-identity gate cannot see: adjacent string literals join only
// across a newline (and "1 - -1" must not collapse into "--1").
func TestOracleTrickyLiterals(t *testing.T) {
	literals := []string{
		"SELECT 'a'\n'b' FROM t", // adjacent literals across newline join in PG
		"SELECT 1 - -1 FROM t",   // must stay "- -1", not become "--1"
	}
	for _, lit := range literals {
		var r SkipReason
		out, ok := LayoutWithReason(lit, "", &r)
		if !ok {
			// Both seeds parse with GoSQLX and must stay accepted: a
			// regression that starts rejecting parseable input would
			// otherwise silently skip the pin.
			t.Fatalf("layout rejected parseable input %q (reason %d: %s)", lit, r, SkipString(r))
		}
		in := oracleParse(t, lit)
		got := oracleParse(t, out)
		if !proto.Equal(in.Interface(), got.Interface()) {
			t.Fatalf("parse tree changed for %q\noutput: %q", lit, out)
		}
	}
}
