// Package processor detects and rewrites SQL inside raw string literals
// (backticks) of Go source files, formatting the SQL with GoSQLX.
package processor

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"

	"github.com/ajitpratap0/GoSQLX/pkg/sql/keywords"
)

// Options controls how literals are detected and formatted.
type Options struct {
	// Dialect feeds the GoSQLX parse gate.
	Dialect keywords.SQLDialect
	// Unit is the indent unit used for layout levels (default "\t").
	Unit string
	// BaseIndent is an extra base indent prepended to every formatted SQL
	// line on top of the literal's derived base (library callers; the CLI
	// leaves it empty and derives the base from the Go line).
	BaseIndent string
	// Debug prints skip reasons to stderr.
	Debug bool
}

// sqlStartKeywords is the list of SQL keywords a literal body must start
// with (after trimming, case-insensitive) to be considered SQL. DDL verbs
// (CREATE, ALTER, DROP, GRANT, REVOKE, TRUNCATE, COMMENT, LOCK, COPY,
// ANALYZE, VACUUM, EXPLAIN) are intentionally absent: DDL is not formatted.
var sqlStartKeywords = []string{
	"SELECT", "INSERT", "UPDATE", "DELETE", "WITH",
	"BEGIN", "COMMIT", "ROLLBACK", "REPLACE", "MERGE", "CALL", "SET", "DO",
}

const ignoreComment = "sqlgofmt:ignore"

type edit struct {
	start, end int
	newBytes   []byte
}

// Process formats SQL inside raw string literals of the given Go source.
// It returns the (possibly unchanged) source and whether it changed.
func Process(filename string, src []byte, opt Options) ([]byte, bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		// The parser error already contains the filename.
		return nil, false, err
	}

	// Collect literals that must never be touched.
	skip := map[ast.Node]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			if n.Tag != nil {
				skip[n.Tag] = true
				debugf(opt, "%s: skip struct tag at %s", filename, fset.Position(n.Tag.Pos()))
			}
		case *ast.BinaryExpr:
			if n.Op == token.ADD {
				markStringLits(skip, n.X)
				markStringLits(skip, n.Y)
			}
		}
		return true
	})

	var edits []edit
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || len(lit.Value) < 2 || lit.Value[0] != '`' {
			return true
		}
		pos := fset.Position(lit.Pos())
		if skip[lit] {
			debugf(opt, "%s:%d: skipped (struct tag or string concatenation)", filename, pos.Line)
			return true
		}
		if hasIgnoreComment(file, fset, pos.Line) {
			debugf(opt, "%s:%d: skipped (%s)", filename, pos.Line, ignoreComment)
			return true
		}
		body := lit.Value[1 : len(lit.Value)-1]
		unit := opt.Unit
		if unit == "" {
			unit = "\t"
		}
		goIndent := goLineIndent(src, fset, lit, unit)
		newVal, ok := formatLiteral(body, goIndent, opt, fmt.Sprintf("%s:%d", filename, pos.Line))
		if !ok || newVal == lit.Value {
			return true
		}
		edits = append(edits, edit{
			start:    fset.Position(lit.Pos()).Offset,
			end:      fset.Position(lit.End()).Offset,
			newBytes: []byte(newVal),
		})
		return true
	})

	if len(edits) == 0 {
		return src, false, nil
	}

	out := make([]byte, len(src))
	copy(out, src)
	// Apply edits in reverse offset order so earlier offsets stay valid.
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		next := make([]byte, 0, len(out)-(e.end-e.start)+len(e.newBytes))
		next = append(next, out[:e.start]...)
		next = append(next, e.newBytes...)
		next = append(next, out[e.end:]...)
		out = next
	}
	return out, true, nil
}

// goLineIndent returns the leading whitespace of the Go line containing
// the literal's opening backtick plus one indent level (the literal's
// base for a literal whose lines would otherwise start at column 0,
// per D10).
func goLineIndent(src []byte, fset *token.FileSet, lit *ast.BasicLit, unit string) string {
	off := fset.Position(lit.Pos()).Offset
	if off < 0 || off > len(src) {
		return unit
	}
	i := off
	for i > 0 && src[i-1] != '\n' {
		i--
	}
	return leadingSpaceTab(string(src[i:off])) + unit
}

// formatLiteral tries to format the SQL body of a raw string literal.
// It returns the full new literal value (with backticks) or ok=false when
// the literal must be left untouched.
func formatLiteral(body string, goIndent string, opt Options, ctx string) (string, bool) {
	// No {{ (Go templates), no \r. Format verbs are handled by the
	// layout engine (masked for parsing, emitted byte-exact). Single-line
	// literals ARE formatted (v4): a literal that entirely fits one line
	// stays one line.
	if strings.ContainsAny(body, "\r") || strings.Contains(body, "{{") {
		debugf(opt, "%s: skipped (contains \\r or Go template {{)", ctx)
		return "", false
	}

	prefix, core, suffix := splitBody(body)
	if strings.TrimSpace(core) == "" {
		debugf(opt, "%s: skipped (empty literal)", ctx)
		return "", false
	}

	// Rule: must start with a SQL keyword. Leading comments (a header
	// above the query is common in real code) do not disqualify the
	// literal. DDL-leading literals are reported as DDL (they are
	// skipped by design, not unsupported).
	probe := stripLeadingComments(core)
	if ddlStartKeywords[firstStatementKeyword(probe)] {
		debugf(opt, "%s: skipped (%s)", ctx, SkipString(ReasonDDL))
		return "", false
	}
	if !startsSQLKeyword(strings.TrimSpace(probe)) {
		debugf(opt, "%s: skipped (does not start with a SQL statement keyword)", ctx)
		return "", false
	}

	unit := opt.Unit
	if unit == "" {
		unit = "\t"
	}
	extra := opt.BaseIndent

	// The literal's base indent. A core that starts mid-Go-line (prefix
	// empty) always uses the Go-line-derived base (D10). Otherwise the
	// common indent of the core is kept (strip/re-apply contract); when
	// it is empty — lines at column 0 — the Go-line base is used so
	// broken lines never land at column 0.
	var litBase string
	if prefix == "" {
		litBase = goIndent
	} else {
		// The applied base is extra+litBase, so a previous run's extra is
		// a PREFIX of the measured indent: strip it from the front.
		common := strings.TrimPrefix(commonIndent(core), extra)
		if common == "" {
			litBase = goIndent
		} else {
			litBase = common
		}
	}
	stripped := stripIndent(core, litBase)

	reason := ReasonOK
	formatted, ok := LayoutWithReason(stripped, unit, &reason, string(opt.Dialect))
	if !ok {
		debugf(opt, "%s: skipped (%s)", ctx, SkipString(reason))
		return "", false
	}

	// Round-trip guard: the layout must be stable on its own output.
	formatted2, ok2 := LayoutWithReason(formatted, unit, &reason, string(opt.Dialect))
	if !ok2 || formatted2 != formatted {
		debugf(opt, "%s: skipped (round-trip mismatch: %s)", ctx, SkipString(reason))
		return "", false
	}

	// Re-apply the base indent. A single-line result keeps the literal on
	// one line: it re-uses the core's own leading whitespace (with any
	// extra already accounted for) so repeated runs are stable. A
	// multiline result's first line stays glued to the backtick when
	// there is no leading newline (it sits mid-Go-line); every other
	// non-blank line gets extra + litBase.
	lines := strings.Split(formatted, "\n")
	var b strings.Builder
	b.WriteString("`")
	b.WriteString(prefix)
	if len(lines) == 1 {
		if prefix != "" {
			b.WriteString(extra)
			b.WriteString(strings.TrimPrefix(leadingSpaceTab(core), extra))
		}
		b.WriteString(lines[0])
	} else {
		for i, line := range lines {
			if i > 0 {
				b.WriteString("\n")
			}
			if line == "" {
				continue
			}
			if i == 0 && prefix == "" {
				b.WriteString(line)
				continue
			}
			b.WriteString(extra)
			b.WriteString(litBase)
			b.WriteString(line)
		}
	}
	b.WriteString(suffix)
	b.WriteString("`")
	return b.String(), true
}

// splitBody splits a literal body into prefix (a leading run of '\n' only —
// it must not carry indent information), core (the SQL text) and suffix
// (trailing whitespace run).
func splitBody(body string) (prefix, core, suffix string) {
	i := 0
	lastNL := -1
	for i < len(body) && isBodyWS(body[i]) {
		if body[i] == '\n' {
			lastNL = i
		}
		i++
	}
	if lastNL >= 0 {
		prefix = body[:lastNL+1]
	}
	// Core starts after the last leading newline: any spaces/tabs between
	// the newline and the first token belong to core (its indent).
	start := i
	if lastNL >= 0 {
		start = lastNL + 1
	}
	rest := body[start:]
	j := len(rest)
	for j > 0 && isBodyWS(rest[j-1]) {
		j--
	}
	return prefix, rest[:j], rest[j:]
}

func isBodyWS(c byte) bool { return c == '\n' || c == ' ' || c == '\t' }

// stripLeadingComments removes leading blank lines, "--" comment lines and
// one /* ... */ block comment run from the start of a literal body, so the
// statement-keyword gate sees the keyword behind a header comment. Block
// comments nest (PostgreSQL rule); an unterminated block comment makes the
// whole remainder comments (the parse gate rejects it later anyway).
func stripLeadingComments(s string) string {
	rest := s
	for {
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		if strings.HasPrefix(trimmed, "--") {
			nl := strings.IndexByte(trimmed, '\n')
			if nl < 0 {
				return ""
			}
			rest = trimmed[nl+1:]
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			depth := 0
			for i := 0; i+1 < len(trimmed); i++ {
				if strings.HasPrefix(trimmed[i:], "/*") {
					depth++
					i++
				} else if strings.HasPrefix(trimmed[i:], "*/") {
					depth--
					if depth == 0 {
						rest = trimmed[i+2:]
						goto next
					}
					i++
				}
			}
			return ""
		}
		return trimmed
	next:
		continue
	}
}

func startsSQLKeyword(s string) bool {
	up := strings.ToUpper(s)
	for _, kw := range sqlStartKeywords {
		if !strings.HasPrefix(up, kw) {
			continue
		}
		rest := up[len(kw):]
		if rest == "" || !isWordChar(rest[0]) {
			return true
		}
	}
	return false
}

func isWordChar(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// commonIndent returns the common leading whitespace (spaces/tabs) prefix
// of all non-blank lines of core.
func commonIndent(core string) string {
	indent := ""
	have := false
	for _, line := range strings.Split(core, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		run := leadingSpaceTab(line)
		if !have {
			indent, have = run, true
			continue
		}
		indent = commonPrefix(indent, run)
		if indent == "" {
			return ""
		}
	}
	return indent
}

func leadingSpaceTab(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}

func commonPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}

// stripIndent removes indent from every non-blank line of core.
func stripIndent(core, indent string) string {
	lines := strings.Split(core, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = strings.TrimPrefix(line, indent)
	}
	return strings.Join(lines, "\n")
}

func markStringLits(set map[ast.Node]bool, n ast.Node) {
	ast.Inspect(n, func(x ast.Node) bool {
		if lit, ok := x.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			set[lit] = true
		}
		return true
	})
}

// hasIgnoreComment reports whether a comment ends on the literal's line or
// the line directly above it and contains "sqlgofmt:ignore".
// Raw comment text is used because Text() strips directive-style comments.
func hasIgnoreComment(file *ast.File, fset *token.FileSet, litLine int) bool {
	for _, cg := range file.Comments {
		endLine := fset.Position(cg.End()).Line
		if endLine != litLine && endLine != litLine-1 {
			continue
		}
		for _, c := range cg.List {
			if strings.Contains(c.Text, ignoreComment) {
				return true
			}
		}
	}
	return false
}

func debugf(opt Options, format string, args ...any) {
	if opt.Debug {
		fmt.Fprintf(os.Stderr, "sqlgofmt: "+format+"\n", args...)
	}
}
