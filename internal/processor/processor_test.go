package processor

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/ajitpratap0/GoSQLX/pkg/sql/keywords"
)

func TestProcess(t *testing.T) {
	tests := []struct {
		name string
		src  string
		// wantChanged: whether the first Process call must report a change.
		wantChanged bool
		// wantSubstr: substrings that must be present in the output.
		wantSubstr []string
		// wantIdempotent: run Process again and require no further change.
		wantIdempotent bool
	}{
		{
			name: "literal starting with a line comment is formatted",
			src: "package p\n\n" +
				"const q = `-- fetch all active workspaces\n" +
				"-- ordered by creation\n" +
				"select id, name from workspaces where active = true order by created_at`\n",
			wantChanged:    true,
			wantSubstr:     []string{"-- fetch all active workspaces\n", "-- ordered by creation\n", "SELECT id, name FROM workspaces WHERE active = TRUE ORDER BY created_at"},
			wantIdempotent: true,
		},
		{
			name: "literal starting with a block comment is formatted",
			src: "package p\n\n" +
				"const q = `/* streaming replicas only */\n" +
				"select host, port from nodes where kind = 'stream'`\n",
			wantChanged:    true,
			wantSubstr:     []string{"/* streaming replicas only */\n", "SELECT host, port FROM nodes WHERE kind = 'stream'"},
			wantIdempotent: true,
		},
		{
			name: "const select fitting one line stays one line",
			src: "package p\n\n" +
				"const q = `\n" +
				"select id, name\n" +
				"from users\n" +
				"where active = true\n" +
				"order by id\n" +
				"`\n",
			wantChanged:    true,
			wantSubstr:     []string{"SELECT id, name FROM users WHERE active = TRUE ORDER BY id"},
			wantIdempotent: true,
		},
		{
			name: "literal inside db.Exec call is formatted",
			src: "package p\n\n" +
				"type DB struct{}\n\n" +
				"func f(db DB) error {\n" +
				"\t_, err := db.Exec(`\n" +
				"select id\n" +
				"  from users\n" +
				"   where id = 1`)\n" +
				"\treturn err\n" +
				"}\n",
			wantChanged:    true,
			wantSubstr:     []string{"SELECT id FROM users WHERE id = 1"},
			wantIdempotent: true,
		},
		{
			name: "skipped: operand of string concatenation",
			src: "package p\n\n" +
				"const q = `\n" +
				"SELECT\n" +
				"  a,\n" +
				"  b` + cols + ` FROM\n" +
				"dual`\n",
			wantChanged: false,
		},
		{
			name: "formatted: fmt.Sprintf format string (verbs survive)",
			src: "package p\n\n" +
				"import \"fmt\"\n\n" +
				"func f(name string) string {\n" +
				"\treturn fmt.Sprintf(`SELECT %s\n" +
				"from t`, name)\n" +
				"}\n",
			wantChanged: true,
			wantSubstr:  []string{"SELECT %s FROM t"},
		},
		{
			name: "skipped: struct tag untouched",
			src: "package p\n\n" +
				"type User struct {\n" +
				"\tID int `db:\"id\"`\n" +
				"}\n",
			wantChanged: false,
		},
		{
			name:        "single-line literal that fits stays one line",
			src:         "package p\n\nconst q = `SELECT * FROM users`\n",
			wantChanged: false,
		},
		{
			name: "single-line literal with AND becomes multiline (D10)",
			src: "package p\n\n" +
				"func f(db DB) error {\n" +
				"\t_, err := db.Exec(`SELECT a, b FROM t WHERE x = 1 AND y = 2`)\n" +
				"\treturn err\n" +
				"}\n",
			wantChanged:    true,
			wantSubstr:     []string{"\n\t\tWHERE x = 1\n\t\t\tAND y = 2"},
			wantIdempotent: true,
		},
		{
			name: "skipped: Go template literal",
			src: "package p\n\n" +
				"const q = `\n" +
				"SELECT *\n" +
				"FROM users\n" +
				"WHERE id = {{.ID}}`\n",
			wantChanged: false,
		},
		{
			name: "skipped: DDL (CREATE) is intentionally not formatted",
			src: "package p\n\n" +
				"const q = `\n" +
				"CREATE TABLE mailbox (id INTEGER PRIMARY KEY)\n" +
				"`\n",
			wantChanged: false,
		},
		{
			name: "skipped: sqlx :::: escaping",
			src: "package p\n\n" +
				"const q = `\n" +
				"SELECT a\n" +
				"FROM t\n" +
				"WHERE b = ::::param`\n",
			wantChanged: false,
		},
		{
			name: "skipped: keyword-looking but invalid SQL",
			src: "package p\n\n" +
				"const doc = `\n" +
				"SELECT ??? not sql\n" +
				"from (((`\n",
			wantChanged: false,
		},
		{
			name: "skipped: sqlgofmt:ignore comment above",
			src: "package p\n\n" +
				"//sqlgofmt:ignore\n" +
				"const q = `\n" +
				"select id\n" +
				"from t`\n",
			wantChanged: false,
		},
		{
			name: "golden: exact formatted output",
			src: "package p\n\n" +
				"const q = `\n" +
				"SELECT  u.id,  u.email\n" +
				"from users u\n" +
				"  left join orders o on o.user_id = u.id\n" +
				"WHERE u.active = true\n" +
				"ORDER BY u.email DESC LIMIT 10`\n",
			wantChanged:    true,
			wantSubstr:     nil, // checked exactly below
			wantIdempotent: true,
		},
		{
			name: "placeholders $1/$2 preserved",
			src: "package p\n\n" +
				"const q = `\n" +
				"select id\n" +
				"from t\n" +
				"where x = $1\n" +
				"  and y = $2`\n",
			wantChanged:    true,
			wantSubstr:     []string{"$1", "$2"},
			wantIdempotent: true,
		},
		{
			name: "quoted identifier preserved",
			src: "package p\n\n" +
				"const q = `\n" +
				"select u.\"user\"  from core.\"user\" u   where u.id = $1 and u.org = $2`\n",
			wantChanged: true,
			wantSubstr:  []string{"core.\"user\"", "$1"},
		},
		{
			name: "v4 indented literal collapses a fitting statement",
			src: "package p\n\n" +
				"func f(db DB) {\n" +
				"\tdb.Query(`\n" +
				"\t\tSELECT id\n" +
				"\t\tFROM t\n" +
				"\t`)\n" +
				"}\n",
			wantChanged:    true,
			wantSubstr:     []string{"\n\t\tSELECT id FROM t\n"},
			wantIdempotent: true,
		},
		{
			name: "indented literal: INSERT column list args-preserved",
			src: "package p\n\n" +
				"func f(db DB) {\n" +
				"\tconst q = `\n" +
				"\t\tINSERT INTO tool_file_changes (\n" +
				"\t\t\tsession_id, message_id\n" +
				"\t\t) VALUES ('a', 'b')`\n" +
				"\t_ = q\n" +
				"}\n",
			wantChanged:    false, // args-preserve: source is a fixed point
			wantSubstr:     nil,   // checked exactly below
			wantIdempotent: true,
		},
		{
			name: "uneven indent: common indent stripped, extra stays in SQL",
			src: "package p\n\n" +
				"const q = `\n" +
				"    select id\n" +
				"      from t\n" +
				"    where x = 1`\n",
			wantChanged:    true,
			wantSubstr:     nil, // checked exactly below
			wantIdempotent: true,
		},
		{
			name: "suffix with closing backtick on own line preserved",
			src: "package p\n\n" +
				"const q = `select id  from t\n" +
				"`\n",
			wantChanged: true,
			wantSubstr:  []string{"SELECT id FROM t\n`"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed, err := Process("test.go", []byte(tt.src), Options{Dialect: keywords.DialectPostgreSQL})
			if err != nil {
				t.Fatalf("Process error: %v", err)
			}
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v\noutput:\n%s", changed, tt.wantChanged, out)
			}

			switch tt.name {
			case "golden: exact formatted output":
				// WHERE starts its own line (no dangling keyword at the
				// end of the SELECT line); ORDER BY/LIMIT glue onto it
				// while they fit (README: clauses glue while fitting).
				want := "package p\n\nconst q = `\n" +
					"\tSELECT u.id, u.email FROM users u LEFT JOIN orders o ON o.user_id = u.id\n" +
					"\tWHERE u.active = TRUE\n" +
					"\tORDER BY u.email DESC LIMIT 10`\n"
				if string(out) != want {
					t.Fatalf("golden mismatch\n got: %q\nwant: %q", string(out), want)
				}
			case "indented literal: INSERT column list args-preserved":
				want := "package p\n\nfunc f(db DB) {\n" +
					"\tconst q = `\n" +
					"\t\tINSERT INTO tool_file_changes (\n" +
					"\t\t\tsession_id, message_id\n" +
					"\t\t) VALUES ('a', 'b')`\n" +
					"\t_ = q\n" +
					"}\n"
				if string(out) != want {
					t.Fatalf("indented literal mismatch\n got: %q\nwant: %q", string(out), want)
				}
				// No SQL line may sit at column 0 inside the literal.
				if !strings.Contains(string(out), "\n\t\tINSERT INTO") ||
					strings.Contains(string(out), "\nINSERT INTO") {
					t.Fatalf("line at column 0 inside indented literal:\n%s", out)
				}
			case "uneven indent: common indent stripped, extra stays in SQL":
				want := "package p\n\nconst q = `\n" +
					"    SELECT id FROM t WHERE x = 1`\n"
				if string(out) != want {
					t.Fatalf("uneven indent mismatch\n got: %q\nwant: %q", string(out), want)
				}
			}

			for _, sub := range tt.wantSubstr {
				if !strings.Contains(string(out), sub) {
					t.Errorf("output missing %q\ngot: %s", sub, out)
				}
			}

			if tt.wantIdempotent {
				out2, changed2, err := Process("test.go", out, Options{Dialect: keywords.DialectPostgreSQL})
				if err != nil {
					t.Fatalf("second Process error: %v", err)
				}
				if changed2 || string(out2) != string(out) {
					t.Errorf("not idempotent: changed=%v\noutput:\n%s", changed2, out2)
				}
			}
		})
	}
}

func TestProcessParseError(t *testing.T) {
	_, _, err := Process("bad.go", []byte("package p\nfunc {"), Options{})
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestProcessEmptyBody(t *testing.T) {
	// Whitespace-only literal must be skipped, not formatted.
	src := "package p\n\nconst q = `\n   \n`\n"
	_, changed, err := Process("test.go", []byte(src), Options{})
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if changed {
		t.Fatal("empty body must not change the file")
	}
}

// TestProcessDDLUntouched pins the DDL gate at the Process level: DDL
// literals are returned byte-exact.
func TestProcessDDLUntouched(t *testing.T) {
	for _, lit := range []string{
		"CREATE INDEX x ON t(c) WHERE cond;",
		"CREATE TABLE mailbox (id INTEGER PRIMARY KEY)",
	} {
		src := "package p\n\nconst q = `\n" + lit + "\n`\n"
		out, changed, err := Process("test.go", []byte(src), Options{Dialect: keywords.DialectPostgreSQL})
		if err != nil {
			t.Fatalf("Process error: %v", err)
		}
		if changed || string(out) != src {
			t.Fatalf("DDL literal %q must be untouched", lit)
		}
	}
}

// TestProcessBaseIndentOption pins the BaseIndent option: it is prepended
// to every formatted line on top of the literal's derived base, and a
// repeated run with the same option is a fixed point.
func TestProcessBaseIndentOption(t *testing.T) {
	src := "package p\n\nconst q = `\nselect id\nfrom t`\n"
	opt := Options{Dialect: keywords.DialectPostgreSQL, BaseIndent: "  "}
	out, changed, err := Process("test.go", []byte(src), opt)
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if !changed {
		t.Fatal("expected a change")
	}
	want := "package p\n\nconst q = `\n  SELECT id FROM t`\n"
	if string(out) != want {
		t.Fatalf("base indent mismatch\n got: %q\nwant: %q", string(out), want)
	}
	out2, changed2, err := Process("test.go", out, opt)
	if err != nil {
		t.Fatalf("second Process error: %v", err)
	}
	if changed2 || string(out2) != want {
		t.Fatalf("BaseIndent is not idempotent: changed=%v\n got: %q\nwant: %q", changed2, string(out2), want)
	}
}

// TestProcessUnitOption pins the unit option at the Process level (G10).
func TestProcessUnitOption(t *testing.T) {
	src := "package p\n\nfunc f(db DB) {\n\tdb.Exec(`SELECT a FROM t WHERE x = 1 AND y = 2`)\n}\n"
	out, changed, err := Process("test.go", []byte(src), Options{Dialect: keywords.DialectPostgreSQL, Unit: "  "})
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if !changed {
		t.Fatal("expected a change")
	}
	want := "package p\n\nfunc f(db DB) {\n\tdb.Exec(`SELECT a FROM t\n\t  WHERE x = 1\n\t    AND y = 2`)\n}\n"
	if string(out) != want {
		t.Fatalf("unit option mismatch\n got: %q\nwant: %q", string(out), want)
	}
	out2, changed2, err := Process("test.go", out, Options{Dialect: keywords.DialectPostgreSQL, Unit: "  "})
	if err != nil {
		t.Fatalf("second Process error: %v", err)
	}
	if changed2 || string(out2) != want {
		t.Fatalf("unit option not idempotent: changed=%v\n got: %q\nwant: %q", changed2, string(out2), want)
	}
}

// TestG9SingleLineLiteralGoParser pins G9: a single-line literal inside a
// Go call becomes multiline with a base indent derived from the Go line,
// and the output still parses as Go.
func TestG9SingleLineLiteralGoParser(t *testing.T) {
	src := "package p\n\n" +
		"func f(db DB) error {\n" +
		"\t_, err := db.Exec(`SELECT a, b FROM t WHERE x = 1 AND y = 2`)\n" +
		"\treturn err\n" +
		"}\n"
	out, changed, err := Process("test.go", []byte(src), Options{Dialect: keywords.DialectPostgreSQL})
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if !changed {
		t.Fatal("expected the single-line literal to be formatted (D10)")
	}
	want := "package p\n\n" +
		"func f(db DB) error {\n" +
		"\t_, err := db.Exec(`SELECT a, b FROM t\n" +
		"\t\tWHERE x = 1\n" +
		"\t\t\tAND y = 2`)\n" +
		"\treturn err\n" +
		"}\n"
	if string(out) != want {
		t.Fatalf("G9 mismatch\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "out.go", out, parser.ParseComments); err != nil {
		t.Fatalf("output does not parse as Go: %v\n%s", err, out)
	}
	out2, changed2, err := Process("test.go", out, Options{Dialect: keywords.DialectPostgreSQL})
	if err != nil {
		t.Fatalf("second Process error: %v", err)
	}
	if changed2 || string(out2) != want {
		t.Fatalf("G9 not idempotent: changed=%v\n got: %q", changed2, string(out2))
	}
}

// TestG13NestingIndependentBodies pins G13: the same literal at different
// Go nesting depths produces identical SQL bodies after stripping each
// literal's own base indent.
func TestG13NestingIndependentBodies(t *testing.T) {
	lit := "SELECT a, b FROM t WHERE x = 1 AND y = 2"
	shallow := "package p\n\nconst q = `" + lit + "`\n"
	deep := "package p\n\nfunc f(db DB) {\n\tdb.Query(`" + lit + "`)\n}\n"
	outS, changed, err := Process("a.go", []byte(shallow), Options{Dialect: keywords.DialectPostgreSQL})
	if err != nil || !changed {
		t.Fatalf("shallow: err=%v changed=%v", err, changed)
	}
	outD, changed, err := Process("b.go", []byte(deep), Options{Dialect: keywords.DialectPostgreSQL})
	if err != nil || !changed {
		t.Fatalf("deep: err=%v changed=%v", err, changed)
	}
	bodyS := extractLiteralBody(string(outS))
	bodyD := extractLiteralBody(string(outD))
	if bodyS == "" || bodyD == "" {
		t.Fatalf("literal bodies not found:\n%s\n---\n%s", outS, outD)
	}
	// Strip each body's own base indent (derived from its Go nesting) and
	// compare: the SQL layout itself must be nesting-independent.
	stripTabs := func(s string) string {
		lines := strings.Split(s, "\n")
		for i, ln := range lines {
			lines[i] = strings.TrimLeft(ln, "\t ")
		}
		return strings.Join(lines, "\n")
	}
	if stripTabs(bodyS) != stripTabs(bodyD) {
		t.Fatalf("SQL bodies differ across nesting depths\n--- shallow ---\n%s\n--- deep ---\n%s", bodyS, bodyD)
	}
}

// extractLiteralBody returns the first backtick literal's content.
func extractLiteralBody(src string) string {
	i := strings.Index(src, "`")
	if i < 0 {
		return ""
	}
	j := strings.Index(src[i+1:], "`")
	if j < 0 {
		return ""
	}
	return src[i+1 : i+1+j]
}
