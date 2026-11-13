package processor

// Token-based SQL layout engine, v4 house style: left-aligned root
// keywords, tab-unit indentation, trailing commas, AND/OR at line start,
// width-80 / <=4-items breaking (synthesized from sqlstyle.guide, the
// Mozilla data-docs guide and dbt conventions). Whitespace, comma
// position and keyword casing only: whitelisted clause keywords are
// emitted UPPER CASE, all other token text is never altered, and no
// token is ever added or dropped (commas are repositioned, never
// inserted). Break decisions
// come exclusively from flat widths and item counts, never from source
// newlines. A clause glues onto the current line only while that line is
// open (a statement start or a fresh clause line whose content glued);
// broken content always starts fresh lines.

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ajitpratap0/GoSQLX/pkg/gosqlx"
	"github.com/ajitpratap0/GoSQLX/pkg/models"
	"github.com/ajitpratap0/GoSQLX/pkg/sql/tokenizer"
)

// verbRe matches printf-style format verbs, including width/precision
// forms such as %10s or %-8.2f, over the full fmt verb alphabet.
var verbRe = regexp.MustCompile(`%(?:%|[-+ #0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z])`)

type atomKind int

const (
	atomToken atomKind = iota
	atomComment
)

type atom struct {
	kind  atomKind
	ttype models.TokenType
	text  string // raw source text (format verbs restored)
	start int    // byte offset in the original SQL text
	end   int
}

// ddlStartKeywords are statement keywords whose literals the formatter
// intentionally leaves untouched: too many dialect shapes, the engine
// would mangle them. See README ("DDL is intentionally not formatted").
var ddlStartKeywords = map[string]bool{
	"CREATE": true, "ALTER": true, "DROP": true, "GRANT": true,
	"REVOKE": true, "TRUNCATE": true, "COMMENT": true, "VACUUM": true,
	"ANALYZE": true, "EXPLAIN": true, "LOCK": true, "COPY": true,
}

// upperKeywords is the curated clause-keyword whitelist: a keyword-class
// atom is uppercased only when its uppercased text is listed here and it
// is not part of a qualified name (not preceded by a period). Author
// identifiers the tokenizer classifies as keywords (hash, key, ...) keep
// their casing.
var upperKeywords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "GROUP BY": true,
	"ORDER BY": true, "HAVING": true, "LIMIT": true, "OFFSET": true,
	"AND": true, "OR": true, "NOT": true, "IN": true, "IS": true,
	"NULL": true, "AS": true, "ON": true, "USING": true, "JOIN": true,
	"INNER": true, "LEFT": true, "RIGHT": true, "FULL": true,
	"OUTER": true, "CROSS": true, "NATURAL": true, "INSERT": true,
	"INTO": true, "VALUES": true, "UPDATE": true, "SET": true,
	"DELETE": true, "WITH": true, "CASE": true, "WHEN": true,
	"THEN": true, "ELSE": true, "END": true, "RETURNING": true,
	"UNION": true, "ALL": true, "EXCEPT": true, "INTERSECT": true,
	"EXISTS": true, "BETWEEN": true, "LIKE": true, "ILIKE": true,
	"DISTINCT": true, "ON CONFLICT": true, "DO": true, "NOTHING": true,
	"FOR": true, "INTERVAL": true, "CAST": true, "ASC": true,
	"DESC": true, "TRUE": true, "FALSE": true, "EXTRACT": true,
	"OVER": true, "PARTITION BY": true,
	// Merged join tokens (the tokenizer emits "left join" as one keyword).
	"INNER JOIN": true, "LEFT JOIN": true, "RIGHT JOIN": true,
	"FULL JOIN": true, "CROSS JOIN": true, "OUTER JOIN": true,
	"NATURAL JOIN": true, "LEFT OUTER JOIN": true, "RIGHT OUTER JOIN": true,
	"FULL OUTER JOIN": true,
}

// upperKeyword applies the casing rule to one keyword-class atom's text:
// whitelist hit and not after a period → UPPER CASE; otherwise author
// casing (merged keywords may carry newlines, so whitespace collapses).
func upperKeyword(text string, afterDot bool) string {
	joined := strings.Join(strings.Fields(text), " ")
	if afterDot || !upperKeywords[strings.ToUpper(joined)] {
		return joined
	}
	return strings.ToUpper(joined)
}

// firstStatementKeyword returns the uppercased first word of s after
// skipping leading whitespace and comments ("" when there is none).
func firstStatementKeyword(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if k := strings.IndexByte(s, '\n'); k >= 0 {
				s = s[k+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if k := strings.Index(s, "*/"); k >= 0 {
				s = s[k+2:]
				continue
			}
			break
		}
		break
	}
	end := 0
	for end < len(s) && isWordChar(s[end]) {
		end++
	}
	return strings.ToUpper(s[:end])
}

// Layout formats sqlText (one or more statements) in the v4 house style:
// left-aligned clause keywords, tab-unit levels, trailing commas in broken
// lists, AND/OR at line start, breaking by flat width and item count. It
// changes whitespace, comma position and keyword casing only: whitelisted
// clause keywords are emitted UPPER CASE, every other token's text is
// kept byte-exact, and no token is added or dropped. It is idempotent.
// The second parameter is the indent UNIT (default "\t");
// every line's indent is unit repeated per level, with no base prefix —
// the caller re-applies the literal's base indent. It returns ok=false
// when the input must be left untouched; skipReason (optional pointer)
// then receives the cause for -debug reporting. The optional dialect
// (default "postgresql") feeds the GoSQLX parse gate.
func Layout(sqlText string, indentStr string, dialect ...string) (string, bool) {
	return layout(sqlText, indentStr, nil, dialect...)
}

// LayoutWithReason is Layout with a mandatory reason out-param.
func LayoutWithReason(sqlText string, indentStr string, reason *SkipReason, dialect ...string) (string, bool) {
	return layout(sqlText, indentStr, reason, dialect...)
}

// SkipString returns the human-readable skip reason for -debug output.
func SkipString(r SkipReason) string {
	switch r {
	case ReasonEmpty:
		return "empty literal"
	case ReasonDDL:
		return "DDL statement (not formatted by design)"
	case ReasonSQLXEscape:
		return "sqlx :::: colon escaping"
	case ReasonParse:
		return "invalid SQL: rejected by the parse gate"
	case ReasonUnsafe:
		return "unsafe to re-layout (token stream would change)"
	case ReasonNestedComment:
		return "nested block comment: tokenizer would mis-tokenize the tail"
	}
	return ""
}

// SkipReason describes why Layout refused to touch a literal.
type SkipReason int

const (
	// ReasonEmpty: the SQL text is blank.
	ReasonEmpty SkipReason = iota
	// ReasonDDL: the first statement keyword is a DDL keyword.
	ReasonDDL
	// ReasonSQLXEscape: the literal contains sqlx `::::` colon escaping.
	ReasonSQLXEscape
	// ReasonParse: the SQL does not parse (input or output gate).
	ReasonParse
	// ReasonUnsafe: the re-laid-out output would change tokens.
	ReasonUnsafe
	// ReasonNestedComment: PostgreSQL block comments nest (/* /* */ */)
	// but the tokenizer does not, so the comment tail would be re-tokenized
	// as SQL code.
	ReasonNestedComment
	// ReasonOK is zero value only; Layout never returns it with ok=false.
	ReasonOK
)

func layout(sqlText string, indentStr string, reason *SkipReason, dialect ...string) (string, bool) {
	fail := func(r SkipReason) (string, bool) {
		if reason != nil {
			*reason = r
		}
		return "", false
	}
	if strings.TrimSpace(sqlText) == "" {
		return fail(ReasonEmpty)
	}
	// DDL and sqlx-escaped literals are intentionally not formatted.
	if ddlStartKeywords[firstStatementKeyword(sqlText)] {
		return fail(ReasonDDL)
	}
	if strings.Contains(sqlText, "::::") {
		return fail(ReasonSQLXEscape)
	}
	if hasNestedBlockComment(sqlText) {
		return fail(ReasonNestedComment)
	}
	d := "postgresql"
	if len(dialect) > 0 && dialect[0] != "" {
		d = dialect[0]
	}

	// Format verbs are replaced with same-length '0' fillers before
	// tokenizing, so `%s`/`%10s` tokenize and parse as plain numbers while
	// every byte offset (and hence every raw source slice) stays valid.
	verbs := verbRe.FindAllStringIndex(sqlText, -1)
	masked := maskSpans(sqlText, verbs)

	atoms, ok := atomize(sqlText, masked)
	if !ok || !parsesOK(masked, d) {
		return fail(ReasonParse)
	}

	unit := indentStr
	if unit == "" {
		unit = "\t"
	}
	out := render(atoms, sqlText, unit)

	// The layout output must parse too (guards comment-line breakage and
	// any structural surprise), and the token stream must be identical:
	// whitespace, comma position and keyword casing only, never token
	// count. Keyword-class atoms compare case-insensitively (casing is
	// normalized to UPPER CASE); everything else is byte-exact.
	outMasked := maskSpans(out, verbRe.FindAllStringIndex(out, -1))
	if !parsesOK(outMasked, d) {
		return fail(ReasonParse)
	}
	outAtoms, ok := atomize(out, outMasked)
	if !ok || len(outAtoms) != len(atoms) {
		return fail(ReasonUnsafe)
	}
	for i := range atoms {
		if !sameAtomText(&atoms[i], &outAtoms[i]) {
			return fail(ReasonUnsafe)
		}
	}
	return out, true
}

// isKeywordClass reports whether an atom is a token the tokenizer
// classifies as a keyword (including merged multi-word keywords and
// data-type words).
func isKeywordClass(a *atom) bool {
	return a.kind == atomToken && a.ttype >= models.TokenTypeKeyword
}

// sameAtomText compares two atoms: kind must match; keyword-class token
// text is case-insensitive, everything else byte-exact.
func sameAtomText(a, b *atom) bool {
	if a.kind != b.kind {
		return false
	}
	if isKeywordClass(a) {
		return strings.EqualFold(a.text, b.text)
	}
	return a.text == b.text
}

// maskSpans replaces each [start,end) span with an equal-length run of '0'.
func maskSpans(src string, spans [][]int) string {
	if len(spans) == 0 {
		return src
	}
	var b strings.Builder
	b.Grow(len(src))
	prev := 0
	for _, sp := range spans {
		b.WriteString(src[prev:sp[0]])
		b.WriteString(strings.Repeat("0", sp[1]-sp[0]))
		prev = sp[1]
	}
	b.WriteString(src[prev:])
	return b.String()
}

// parsesOK reports whether masked SQL parses under the given dialect.
func parsesOK(masked, dialect string) bool {
	_, err := gosqlx.ParseTree(context.Background(), masked, gosqlx.WithDialect(dialect))
	return err == nil
}

// scanState is the lexer context of hasNestedBlockComment.
type scanState int

const (
	scanNormal scanState = iota
	scanLineComment
	scanBlockComment
	scanSingleQuote
	scanDoubleQuote
)

// hasNestedBlockComment reports whether a block comment is nested inside
// another block comment. PostgreSQL nests /* /* */ */; the tokenizer does
// not (it stops at the first */), so the tail would be re-tokenized as
// SQL code. "/*" and "*/" inside string literals, quoted identifiers and
// dollar-quoted strings do not count. Single-quote state follows ''
// doubling; E'...\'...' backslash escapes are not tracked (such literals
// are rare and the parse gate still applies).
func hasNestedBlockComment(sql string) bool {
	state := scanNormal
	depth := 0
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch state {
		case scanLineComment:
			if c == '\n' {
				state = scanNormal
			}
		case scanBlockComment:
			switch {
			case c == '/' && strings.HasPrefix(sql[i:], "/*"):
				depth++
				if depth > 1 {
					return true
				}
				i++
			case strings.HasPrefix(sql[i:], "*/"):
				depth--
				if depth == 0 {
					state = scanNormal
				}
				i++
			}
		case scanSingleQuote:
			if c == '\'' {
				if strings.HasPrefix(sql[i+1:], "'") {
					i++
				} else {
					state = scanNormal
				}
			}
		case scanDoubleQuote:
			if c == '"' {
				if strings.HasPrefix(sql[i+1:], "\"") {
					i++
				} else {
					state = scanNormal
				}
			}
		case scanNormal:
			switch {
			case strings.HasPrefix(sql[i:], "--"):
				state = scanLineComment
				i++
			case strings.HasPrefix(sql[i:], "/*"):
				state = scanBlockComment
				depth = 1
				i++
			case c == '\'':
				state = scanSingleQuote
			case c == '"':
				state = scanDoubleQuote
			case c == '$':
				// A dollar tag cannot follow an identifier character:
				// "a$b" is one identifier, not a dollar quote.
				if i > 0 && isDollarIdentByte(sql[i-1]) {
					continue
				}
				tag, ok := dollarTagAt(sql, i)
				if !ok {
					continue
				}
				open := len(tag) + 2 // $tag$
				closeIdx := strings.Index(sql[i+open:], "$"+tag+"$")
				if closeIdx < 0 {
					// Unterminated dollar quote: not a dollar-quote
					// context we can trust; keep scanning so a nested
					// comment later in the text is still caught (the
					// parse gate rejects the unterminated quote anyway).
					continue
				}
				i += open + closeIdx + len(tag) + 1
			}
		}
	}
	return false
}

// dollarTagAt parses a dollar-quote opening delimiter at sql[i] == '$':
// "$$" (empty tag) or "$tag$" with a tag of letters, digits or underscore.
func dollarTagAt(sql string, i int) (tag string, ok bool) {
	j := i + 1
	for j < len(sql) && (sql[j] == '_' || isASCIILetterOrDigit(sql[j])) {
		j++
	}
	if j >= len(sql) || sql[j] != '$' {
		return "", false
	}
	return sql[i+1 : j], true
}

func isASCIILetterOrDigit(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// isDollarIdentByte reports whether c can be part of an identifier that
// may legally contain '$' (PostgreSQL identifiers allow $ after the
// first character).
func isDollarIdentByte(c byte) bool {
	return isASCIILetterOrDigit(c) || c == '_' || c == '$'
}

// atomize tokenizes masked SQL and merges tokens with comments into a
// position-sorted atom stream. Token text is always taken from the original
// source so format verbs survive byte-exact.
func atomize(src, masked string) ([]atom, bool) {
	tkz := tokenizer.GetTokenizer()
	toks, err := tkz.Tokenize([]byte(masked))
	// Comments must be copied before the tokenizer returns to the pool.
	comments := make([]models.Comment, len(tkz.Comments))
	copy(comments, tkz.Comments)
	tokenizer.PutTokenizer(tkz)
	if err != nil {
		return nil, false
	}

	// Token positions are (line, byte-column) pairs; build a line-start table.
	starts := []int{0}
	for i := 0; i < len(masked); i++ {
		if masked[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	off := func(l models.Location) int {
		if l.Line < 1 || l.Line > len(starts) {
			return -1
		}
		o := starts[l.Line-1] + l.Column - 1
		if o < 0 || o > len(masked) {
			return -1
		}
		return o
	}

	atoms := make([]atom, 0, len(toks)+len(comments))
	var prevTok *atom // previous token atom (whitespace never reaches here)
	for _, t := range toks {
		if t.Token.Type == models.TokenTypeEOF || t.Token.Type == models.TokenTypeWhitespace {
			continue
		}
		s, e := off(t.Start), off(t.End)
		if s < 0 || e < s {
			return nil, false
		}
		text := src[s:e]
		if t.Token.Type >= models.TokenTypeKeyword {
			// Keyword-classified tokens are uppercased only when the
			// spelling is on the whitelist and not after a dot (sec.key
			// keeps author casing); everything else is never re-cased.
			text = upperKeyword(text, prevTok != nil && prevTok.ttype == models.TokenTypePeriod)
		}
		atoms = append(atoms, atom{kind: atomToken, ttype: t.Token.Type, text: text, start: s, end: e})
		prevTok = &atoms[len(atoms)-1]
	}
	for _, c := range comments {
		s, e := off(c.Start), off(c.End)
		if s < 0 || e < s {
			return nil, false
		}
		atoms = append(atoms, atom{kind: atomComment, text: strings.TrimRight(src[s:e], "\r\n"), start: s, end: e})
	}
	sort.Slice(atoms, func(i, j int) bool { return atoms[i].start < atoms[j].start })
	return atoms, true
}

// keyword returns the uppercased text of a token atom ("" for comments).
func keyword(a *atom) string {
	if a.kind == atomComment {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(a.text))
}

// joinModifier words glue into one JOIN unit; bare LEFT/RIGHT/... only
// start a unit when followed by another modifier or JOIN.
var joinModifier = map[string]bool{
	"JOIN": true, "INNER JOIN": true, "LEFT JOIN": true, "RIGHT JOIN": true,
	"FULL JOIN": true, "CROSS JOIN": true, "OUTER JOIN": true, "NATURAL JOIN": true,
	"LEFT OUTER JOIN": true, "RIGHT OUTER JOIN": true, "FULL OUTER JOIN": true,
	"LEFT": true, "RIGHT": true, "FULL": true, "INNER": true,
	"OUTER": true, "CROSS": true, "NATURAL": true,
}

// isBareModifier reports whether the atom is a lone join modifier word
// (LEFT/RIGHT/FULL/OUTER/...) whose JOIN continuation follows.
func isBareModifier(a *atom) bool {
	kw := keyword(a)
	return joinModifier[kw] && !strings.Contains(kw, "JOIN")
}

// isFuncName reports whether LPAREN should glue to this atom
// (function-call style, e.g. `count(...)` but not `FROM (`).
func isFuncName(a *atom) bool {
	switch a.ttype {
	case models.TokenTypeCount, models.TokenTypeSum,
		models.TokenTypeAvg, models.TokenTypeMin, models.TokenTypeMax,
		models.TokenTypeArray, models.TokenTypeUsing:
		return true
	}
	return false
}

// needSpace reports whether a single space belongs between prev and cur.
// prev2 is the atom before prev; src is the raw SQL text (used to preserve
// the author's spacing for identifier "(" pairs).
func needSpace(prev, prev2, cur *atom, src string) bool {
	if prev == nil || cur == nil {
		return false
	}
	pt, ct := prev.ttype, cur.ttype
	if cur.text == "," || cur.text == ";" ||
		ct == models.TokenTypeRParen || ct == models.TokenTypeRBracket {
		return false
	}
	if prev.kind == atomComment || cur.kind == atomComment {
		return true
	}
	if pt == models.TokenTypeLParen || pt == models.TokenTypeLBracket {
		return false
	}
	if pt == models.TokenTypePeriod || ct == models.TokenTypePeriod {
		return false
	}
	if pt == models.TokenTypeDoubleColon || ct == models.TokenTypeDoubleColon {
		return false
	}
	// JSON extraction operators keep spaces (house style); arrows glue.
	if pt == models.TokenTypeHashLongArrow || ct == models.TokenTypeHashLongArrow ||
		pt == models.TokenTypeHashArrow || ct == models.TokenTypeHashArrow {
		return true
	}
	if pt == models.TokenTypeArrow || ct == models.TokenTypeArrow ||
		pt == models.TokenTypeLongArrow || ct == models.TokenTypeLongArrow {
		return false
	}
	if ct == models.TokenTypeLParen {
		if pt == models.TokenTypeIdentifier {
			// schema.table ( keeps its space; for a bare identifier the
			// table-name vs function-name distinction needs a parse, so
			// the source spacing is preserved (gofmt philosophy).
			if prev2 != nil && prev2.ttype == models.TokenTypePeriod {
				return true
			}
			return strings.IndexAny(src[prev.end:cur.start], " \t\r\n") >= 0
		}
		if isFuncName(prev) {
			return false
		}
		// Keyword before "(": keep the source spacing too — it covers
		// function-like keywords (replace(...)) as well as structural
		// VALUES ( / ON CONFLICT ( / EXISTS ( which are spaced in source.
		return strings.IndexAny(src[prev.end:cur.start], " \t\r\n") >= 0
	}
	if ct == models.TokenTypeLBracket {
		return false
	}
	return true
}

// matchParen returns the index of the RParen matching the LParen at i,
// or -1 when unbalanced.
func matchParen(atoms []atom, i int) int {
	depth := 0
	for j := i; j < len(atoms); j++ {
		a := atoms[j]
		if a.kind == atomComment {
			continue
		}
		switch a.ttype {
		case models.TokenTypeLParen:
			depth++
		case models.TokenTypeRParen:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// matchCase returns the index of the END matching the CASE at i,
// or -1 when unbalanced.
func matchCase(atoms []atom, i int) int {
	depth := 0
	for j := i; j < len(atoms); j++ {
		a := atoms[j]
		if a.kind == atomComment {
			continue
		}
		switch keyword(&a) {
		case "CASE":
			depth++
		case "END":
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

type frameKind int

const (
	frameSubquery frameKind = iota
	frameArgs
)

// frameKindOf classifies a paren frame by its first non-comment atom.
func frameKindOf(inner []atom) frameKind {
	for i := range inner {
		if inner[i].kind == atomComment {
			continue
		}
		switch keyword(&inner[i]) {
		case "SELECT", "WITH", "INSERT", "UPDATE", "DELETE":
			return frameSubquery
		}
		return frameArgs
	}
	return frameArgs
}

// notAClause reports whether a clause-keyword spelling is really an
// expression: replace(...)/set_config(...) calls and the type/join
// modifiers "WITH TIME ZONE" / "WITH ORDINALITY".
func notAClause(atoms []atom, i int) bool {
	switch keyword(&atoms[i]) {
	case "REPLACE", "SET":
		return i+1 < len(atoms) && atoms[i+1].ttype == models.TokenTypeLParen
	case "WITH":
		if i+1 >= len(atoms) {
			return false
		}
		if keyword(&atoms[i+1]) == "RECURSIVE" {
			return false
		}
		// CTE: WITH name AS ( / WITH name (cols) AS (
		if i+2 < len(atoms) && keyword(&atoms[i+2]) == "AS" {
			return false
		}
		if i+3 < len(atoms) && atoms[i+2].ttype == models.TokenTypeLParen {
			if j := matchParen(atoms, i+2); j >= 0 && j+1 < len(atoms) && keyword(&atoms[j+1]) == "AS" {
				return false
			}
		}
		return true
	}
	return false
}

// isJoinUnitStart reports whether atoms[i] begins a JOIN unit.
func isJoinUnitStart(atoms []atom, i int) bool {
	if atoms[i].kind == atomComment {
		return false
	}
	kw := keyword(&atoms[i])
	if !joinModifier[kw] {
		return false
	}
	if kw == "JOIN" || strings.Contains(kw, "JOIN") {
		return true
	}
	// bare LEFT/RIGHT/...: a JOIN unit only when a modifier/JOIN follows
	// (LEFT(...) is a function call).
	return i+1 < len(atoms) && joinModifier[keyword(&atoms[i+1])]
}

type clauseKind int

const (
	clList       clauseKind = iota // SELECT / SET / RETURNING / GROUP BY / ORDER BY / VALUES: keyword-alone list
	clCond                         // WHERE / HAVING
	clFrom                         // FROM with JOIN segments
	clInsert                       // INSERT INTO / REPLACE INTO with column-list frame
	clUpdate                       // UPDATE <table>
	clDelete                       // DELETE FROM <table>
	clWith                         // WITH name AS (...), ...
	clSetop                        // UNION / EXCEPT / INTERSECT [ALL]: always own line
	clRowLock                      // FOR UPDATE [SKIP LOCKED]: always own line
	clOnConflict                   // ON CONFLICT (...): always own line
	clTail                         // LIMIT / OFFSET / FOR SHARE / FOR NO KEY UPDATE
	clHead                         // DO / BEGIN / COMMIT / ROLLBACK / CALL / MERGE
)

// clauseKindAt classifies a clause-keyword atom and returns the one-past
// end of the glued keyword unit (INSERT INTO, UNION ALL, SELECT DISTINCT,
// ON CONFLICT, FOR UPDATE SKIP LOCKED, ...).
func clauseKindAt(stmt []atom, i int) (clauseKind, int, bool) {
	a := &stmt[i]
	if a.kind == atomComment {
		return 0, 0, false
	}
	kw := keyword(a)
	switch kw {
	case "SELECT":
		end := i + 1
		for end < len(stmt) {
			k := keyword(&stmt[end])
			if k == "DISTINCT" || k == "ALL" {
				end++
				continue
			}
			break
		}
		return clList, end, true
	case "FROM":
		return clFrom, i + 1, true
	case "WHERE", "HAVING":
		return clCond, i + 1, true
	case "GROUP BY", "ORDER BY":
		return clList, i + 1, true
	case "LIMIT", "OFFSET":
		return clTail, i + 1, true
	case "INSERT", "REPLACE":
		if notAClause(stmt, i) {
			return 0, 0, false
		}
		end := i + 1
		if end < len(stmt) && keyword(&stmt[end]) == "INTO" {
			end++
		}
		return clInsert, end, true
	case "VALUES":
		return clList, i + 1, true
	case "SET":
		if notAClause(stmt, i) {
			return 0, 0, false
		}
		return clList, i + 1, true
	case "UPDATE":
		return clUpdate, i + 1, true
	case "DELETE":
		end := i + 1
		if end < len(stmt) && keyword(&stmt[end]) == "FROM" {
			end++
		}
		return clDelete, end, true
	case "RETURNING":
		return clList, i + 1, true
	case "WITH":
		if notAClause(stmt, i) {
			return 0, 0, false
		}
		end := i + 1
		if end < len(stmt) && keyword(&stmt[end]) == "RECURSIVE" {
			end++
		}
		return clWith, end, true
	case "UNION", "EXCEPT", "INTERSECT":
		end := i + 1
		if end < len(stmt) && keyword(&stmt[end]) == "ALL" {
			end++
		}
		return clSetop, end, true
	case "ON":
		if i+1 < len(stmt) && keyword(&stmt[i+1]) == "CONFLICT" {
			return clOnConflict, i + 2, true
		}
		return 0, 0, false
	case "DO", "BEGIN", "COMMIT", "ROLLBACK", "CALL", "MERGE":
		return clHead, i + 1, true
	case "FOR":
		return forKind(stmt, i)
	}
	return 0, 0, false
}

// forKind classifies row-lock clauses: FOR UPDATE [SKIP LOCKED] is an
// always-own-line unit; FOR SHARE / FOR [NO] KEY UPDATE/SHARE are tails
// that glue when they fit.
func forKind(stmt []atom, i int) (clauseKind, int, bool) {
	j := i + 1
	if j >= len(stmt) {
		return 0, 0, false
	}
	switch keyword(&stmt[j]) {
	case "UPDATE":
		j++
		for j < len(stmt) {
			k := keyword(&stmt[j])
			if k == "SKIP" || k == "LOCKED" {
				j++
				continue
			}
			break
		}
		return clRowLock, j, true
	case "SHARE", "NO", "KEY":
		for j < len(stmt) {
			k := keyword(&stmt[j])
			if k == "SHARE" || k == "NO" || k == "KEY" {
				j++
				continue
			}
			break
		}
		return clTail, j, true
	}
	return 0, 0, false
}

type clauseSeg struct {
	kind  clauseKind
	start int // first atom (keyword)
	kwEnd int // one past the glued keyword unit
	end   int // one past the clause's atoms
}

// scanClauses segments a statement into top-level clauses. Paren frames
// and CASE..END spans are skipped wholesale, so keywords inside them are
// never clause boundaries. JOIN / ON / USING / AND / OR flow into the
// surrounding clause's rest.
func scanClauses(stmt []atom) []clauseSeg {
	var segs []clauseSeg
	i := 0
	for i < len(stmt) {
		a := &stmt[i]
		if a.kind == atomComment {
			i++
			continue
		}
		if a.ttype == models.TokenTypeLParen {
			j := matchParen(stmt, i)
			if j < 0 {
				i++
				continue
			}
			i = j + 1
			continue
		}
		if keyword(a) == "CASE" {
			j := matchCase(stmt, i)
			if j < 0 {
				i++
				continue
			}
			i = j + 1
			continue
		}
		if kind, kwEnd, ok := clauseKindAt(stmt, i); ok {
			// DO NOTHING / DO UPDATE glue into a preceding ON CONFLICT
			// unit (D9) instead of forming their own clause.
			if kind == clHead && len(segs) > 0 && segs[len(segs)-1].kind == clOnConflict {
				i = kwEnd
				continue
			}
			segs = append(segs, clauseSeg{kind: kind, start: i, kwEnd: kwEnd})
			i = kwEnd
			continue
		}
		i++
	}
	for si := range segs {
		if si+1 < len(segs) {
			segs[si].end = segs[si+1].start
		} else {
			segs[si].end = len(stmt)
		}
	}
	return segs
}

// splitOn splits atoms into segments separated by top-level separator
// atoms matching want. Paren frames and CASE..END spans never split; an
// AND directly after BETWEEN (its range operator) is not a separator.
func splitOn(atoms []atom, want func(a *atom) bool) ([][]atom, []*atom) {
	var segs [][]atom
	var seps []*atom
	cur := []atom{}
	pendBetween := false
	i := 0
	for i < len(atoms) {
		a := &atoms[i]
		if a.kind == atomComment {
			cur = append(cur, *a)
			i++
			continue
		}
		if a.ttype == models.TokenTypeLParen {
			j := matchParen(atoms, i)
			if j < 0 {
				cur = append(cur, *a)
				i++
				continue
			}
			cur = append(cur, atoms[i:j+1]...)
			i = j + 1
			continue
		}
		kw := keyword(a)
		if kw == "CASE" {
			j := matchCase(atoms, i)
			if j < 0 {
				cur = append(cur, *a)
				i++
				continue
			}
			cur = append(cur, atoms[i:j+1]...)
			i = j + 1
			continue
		}
		if kw == "BETWEEN" {
			pendBetween = true
		}
		if want(a) {
			if kw == "AND" && pendBetween {
				pendBetween = false
			} else {
				seps = append(seps, a)
				segs = append(segs, cur)
				cur = []atom{}
				i++
				continue
			}
		}
		cur = append(cur, *a)
		i++
	}
	segs = append(segs, cur)
	return segs, seps
}

func isComma(a *atom) bool { return a.kind == atomToken && a.text == "," }

func isAndOr(a *atom) bool {
	if a.kind == atomComment {
		return false
	}
	kw := keyword(a)
	return kw == "AND" || kw == "OR"
}

func isSemi(a *atom) bool { return a.kind == atomToken && a.ttype == models.TokenTypeSemicolon }

// widthLimit is the max line width in runes (indent units included).
const widthLimit = 80

type layouter struct {
	src       string
	unit      string
	unitW     int // rune width of the indent unit
	out       strings.Builder
	cur       strings.Builder
	curHasTok bool
	curRunes  int   // runes of content on the pending line (indent excluded)
	curLevel  int   // indent level of the pending line
	lastA     *atom // last atom written on the pending line
	last2A    *atom // atom before lastA
	outEndsNL bool  // out ends with a newline
	// lineOpen reports whether the pending line is a clause line that the
	// next clause may extend (statement start, or a fresh clause line
	// whose content glued). Broken content (items at level+1, AND lines,
	// frame bodies) closes the line.
	lineOpen bool
	// pendingBreak forces the next clause onto a fresh line (set when a
	// frame's "(" just terminated its line, per D6).
	pendingBreak bool
}

func render(atoms []atom, src, unit string) string {
	l := &layouter{src: src, unit: unit, unitW: utf8.RuneCountInString(unit)}
	l.lineOpen = true
	l.emitBlock(atoms, 0)
	l.flush()
	return l.out.String()
}

func (l *layouter) flush() {
	if l.curHasTok {
		s := strings.TrimRight(l.cur.String(), " \t")
		l.out.WriteString(s)
		l.outEndsNL = strings.HasSuffix(s, "\n")
		l.cur.Reset()
		l.curHasTok = false
	}
}

// br starts a fresh line at the given level: the level indent in units.
// An indent-only pending line is superseded rather than duplicated.
func (l *layouter) br(level int) {
	if l.curHasTok {
		l.flush()
		l.out.WriteString("\n")
		l.outEndsNL = true
	} else if !l.outEndsNL && l.out.Len() > 0 {
		l.out.WriteString("\n")
		l.outEndsNL = true
	}
	l.cur.Reset()
	l.cur.WriteString(strings.Repeat(l.unit, level))
	l.curRunes = 0
	l.curLevel = level
	l.curHasTok = false
	l.lastA, l.last2A = nil, nil
	l.pendingBreak = false
}

// blankLine ends the pending line and emits one empty line (D8: between
// statements and CTE definitions only — the callers enforce that).
func (l *layouter) blankLine() {
	if l.curHasTok {
		l.out.WriteString(strings.TrimRight(l.cur.String(), " \t"))
		l.cur.Reset()
		l.curHasTok = false
	}
	l.out.WriteString("\n")
	l.out.WriteString("\n")
	l.outEndsNL = true
}

func (l *layouter) writeRaw(s string) {
	l.cur.WriteString(s)
	l.curRunes += utf8.RuneCountInString(s)
	l.curHasTok = true
}

// onLineComment reports whether the pending line ends with a "--" line
// comment: anything written after it would be swallowed by the comment.
func (l *layouter) onLineComment() bool {
	return l.curHasTok && l.lastA != nil && l.lastA.kind == atomComment &&
		strings.HasPrefix(l.lastA.text, "--")
}

func (l *layouter) writeAtom(a *atom) {
	if l.onLineComment() {
		// Never continue a "--" comment line: break first.
		l.br(l.curLevel)
	}
	if l.curHasTok && needSpace(l.lastA, l.last2A, a, l.src) {
		l.writeRaw(" ")
	}
	l.cur.WriteString(a.text)
	l.curRunes += utf8.RuneCountInString(a.text)
	l.curHasTok = true
	l.last2A, l.lastA = l.lastA, a
}

func (l *layouter) writeInline(atoms []atom) {
	for i := range atoms {
		l.writeAtom(&atoms[i])
	}
}

// fitsAhead reports whether atoms of flat width flat (runes) still fit on
// the current line at the given level: level indent units + current line
// content + the separator gap + flat <= 80 runes.
func (l *layouter) fitsAhead(level, flat int, first *atom) bool {
	if l.onLineComment() {
		return false // nothing may be appended to a "--" comment line
	}
	gap := 0
	if l.curHasTok && first != nil && needSpace(l.lastA, l.last2A, first, l.src) {
		gap = 1
	}
	return level*l.unitW+l.curRunes+gap+flat <= widthLimit
}

// flatHard measures the flat single-line width (runes) of atoms and
// reports whether the range contains a hard-break trigger: a comment or a
// multiline args frame (at any depth).
func (l *layouter) flatHard(atoms []atom) (int, bool) {
	hard := false
	width := 0
	var prev, prev2 *atom
	i := 0
	for i < len(atoms) {
		a := &atoms[i]
		if a.kind == atomComment {
			hard = true
			prev2, prev = prev, a
			i++
			continue
		}
		if a.ttype == models.TokenTypeLParen {
			j := matchParen(atoms, i)
			if j < 0 {
				if needSpace(prev, prev2, a, l.src) {
					width++
				}
				width += utf8.RuneCountInString(a.text)
				prev2, prev = prev, a
				i++
				continue
			}
			rp := &atoms[j]
			inner := atoms[i+1 : j]
			if frameKindOf(inner) == frameArgs && strings.Contains(l.src[a.end:rp.start], "\n") {
				hard = true
			}
			w, h := l.flatHard(inner)
			if h {
				hard = true
			}
			if needSpace(prev, prev2, a, l.src) {
				width++
			}
			width += 1 + w + 1 // "(...)"
			prev2, prev = prev, rp
			i = j + 1
			continue
		}
		if needSpace(prev, prev2, a, l.src) {
			width++
		}
		width += utf8.RuneCountInString(a.text)
		prev2, prev = prev, a
		i++
	}
	return width, hard
}

func (l *layouter) flatLen(atoms []atom) int {
	w, _ := l.flatHard(atoms)
	return w
}

func (l *layouter) groupHard(atoms []atom) bool {
	_, h := l.flatHard(atoms)
	return h
}

// emitBlock lays out statements separated by top-level semicolons, with a
// blank line between statements (D8).
func (l *layouter) emitBlock(atoms []atom, level int) {
	parts, semis := splitOn(atoms, isSemi)
	nonEmpty := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if len(parts[i]) > 0 {
			nonEmpty = i
			break
		}
	}
	if nonEmpty < 0 {
		return
	}
	for i := 0; i <= nonEmpty; i++ {
		l.emitStmt(parts[i], level)
		if i < len(semis) {
			l.writeRaw(";")
			if i < nonEmpty {
				l.blankLine()
				l.lineOpen = true
			}
		}
	}
}

// emitStmt lays out one statement by its top-level clauses.
func (l *layouter) emitStmt(stmt []atom, level int) {
	segs := scanClauses(stmt)
	if len(segs) == 0 {
		if l.pendingBreak {
			// A frame's "(" just terminated its line (D6).
			l.br(level)
			l.lineOpen = true
			l.pendingBreak = false
		}
		l.emitExpr(stmt, level)
		return
	}
	if segs[0].start > 0 {
		l.emitExpr(stmt[:segs[0].start], level)
	}
	for si := range segs {
		l.emitClause(stmt, &segs[si], level)
	}
}

// settle breaks to a fresh line when the previous emission demanded it
// (a frame's "(" just terminated its line, per D6).
func (l *layouter) settle(level int) {
	if l.pendingBreak {
		l.br(level)
		l.lineOpen = true
		l.pendingBreak = false
	}
}

// openClause puts the emitter on a fresh clause line when the current
// line cannot be extended.
func (l *layouter) openClause(level int) {
	l.settle(level)
	if !l.lineOpen {
		l.br(level)
		l.lineOpen = true
	}
}

// argsFrameStart reports whether atoms begins (skipping comments) with a
// top-level multiline args-preserve paren frame, returning the frame's
// extent [lp, one-past-rp).
func argsFrameStart(l *layouter, atoms []atom) (int, int, bool) {
	i := 0
	for i < len(atoms) && atoms[i].kind == atomComment {
		i++
	}
	if i >= len(atoms) || atoms[i].ttype != models.TokenTypeLParen {
		return 0, 0, false
	}
	j := matchParen(atoms, i)
	if j < 0 {
		return 0, 0, false
	}
	if frameKindOf(atoms[i+1:j]) != frameArgs ||
		!strings.Contains(l.src[atoms[i].end:atoms[j].start], "\n") {
		return 0, 0, false
	}
	return i, j + 1, true
}

func (l *layouter) emitClause(stmt []atom, seg *clauseSeg, level int) {
	l.settle(level)
	kw := stmt[seg.start:seg.kwEnd]
	rest := stmt[seg.kwEnd:seg.end]
	flatAll, hard := l.flatHard(stmt[seg.start:seg.end])

	switch seg.kind {
	case clList:
		// A clause keyword directly followed by a multiline args-preserve
		// frame (VALUES ( ... )) keeps "(" glued to the keyword line,
		// content at level+1 and ")" at the keyword's level (D6). A
		// comment before "(" falls back to the generic path so it is
		// never dropped.
		if fs, fe, ok := argsFrameStart(l, rest); ok && fs == 0 && fe == len(rest) {
			l.openClause(level)
			l.writeInline(kw)
			lp, rp := &rest[fs], &rest[fe-1]
			l.writeAtom(lp)
			l.lineOpen = false
			l.emitArgsLines(lp, rp, rest[fs+1:fe-1], level)
			return
		}
		items, seps := splitOn(rest, isComma)
		l.openClause(level)
		fit := len(items) > 0 && !hard && len(items) <= 4 &&
			l.fitsAhead(level, flatAll, &stmt[seg.start])
		if !fit {
			// Broken list: the keyword sits alone on its line (G2), items
			// one per line at level+1 with the source's own commas in
			// trailing position (never added).
			l.br(level)
			l.lineOpen = true
		}
		l.writeInline(kw)
		if !fit {
			l.emitCommaItems(items, seps, level)
			return
		}
		l.writeInline(rest)

	case clCond:
		conds, seps := splitOn(rest, isAndOr)
		// D4: a WHERE/HAVING chain with AND/OR (or a broken chain)
		// always starts a new line; only a fitting single condition glues.
		// fit is computed BEFORE the keyword is written: flatAll includes
		// the keyword, and fitsAhead adds the gap relative to the current
		// line — computing it after writeInline would double-count the
		// keyword width and leave a dangling WHERE at the line end.
		multi := len(conds) > 1
		condHard := l.groupHard(rest)
		fit := !condHard && len(conds) <= 4 && len(conds) > 0 && l.fitsAhead(level, flatAll, &stmt[seg.start])
		if multi || condHard || !fit {
			l.br(level)
			l.lineOpen = true
		} else {
			l.openClause(level)
		}
		l.writeInline(kw)
		if len(conds) == 0 {
			return
		}
		if fit && !multi {
			l.writeInline(rest)
			return
		}
		l.lineOpen = false
		if fit {
			// The chain broke to a fresh line (keyword included) and
			// every condition fits: keyword line carries the first
			// condition, each AND/OR starts its own line at level+1.
			l.writeInline(conds[0])
			for i := 1; i < len(conds); i++ {
				l.br(level + 1)
				l.writeAtom(seps[i-1])
				l.writeInline(conds[i])
			}
			return
		}
		for i := range conds {
			if i > 0 {
				l.writeAtom(seps[i-1])
			}
			l.emitExpr(conds[i], level+1)
			if i < len(conds)-1 {
				l.br(level + 1)
			}
		}

	case clFrom:
		l.openClause(level)
		if len(rest) > 0 {
			joins0 := splitJoinSegs(rest)
			fitWhole0 := !hard && len(joins0) <= 3 && l.fitsAhead(level, flatAll, &stmt[seg.start])
			if !fitWhole0 {
				// If the table base cannot fit on the current line
				// (keyword included) but fits on a fresh one, move the
				// whole clause there instead of dangling the keyword at
				// the end of the previous line or breaking the list.
				baseEnd0 := len(rest)
				if len(joins0) > 0 {
					baseEnd0 = joinBaseEnd(rest)
				}
				base0 := rest[:baseEnd0]
				if len(base0) > 0 && !l.groupHard(base0) &&
					!l.fitsAhead(level, l.flatLen(kw)+1+l.flatLen(base0), &stmt[seg.start]) &&
					level*l.unitW+l.flatLen(kw)+1+l.flatLen(base0) <= widthLimit {
					l.br(level)
					l.lineOpen = true
				}
			}
		}
		if !l.lineOpen {
			l.br(level)
		}
		l.lineOpen = true
		l.writeInline(kw)
		if len(rest) == 0 {
			return
		}
		joins := splitJoinSegs(rest)
		fitWhole := !hard && len(joins) <= 3 && l.fitsAhead(level, flatAll, &stmt[seg.start])
		for _, js := range joins {
			if _, on, hasOn := splitJoinOn(js); hasOn {
				if conds, _ := splitOn(on[1:], isAndOr); len(conds) > 1 {
					fitWhole = false
				}
			}
		}
		if fitWhole {
			l.writeInline(rest)
			return
		}
		l.lineOpen = false
		baseEnd := len(rest)
		if len(joins) > 0 {
			baseEnd = joinBaseEnd(rest)
		}
		base := rest[:baseEnd]
		if len(base) > 0 {
			baseHard := l.groupHard(base)
			baseFlat := l.flatLen(base)
			if !baseHard && l.fitsAhead(level, baseFlat, &base[0]) {
				l.writeInline(base)
			} else if !baseHard && level*l.unitW+baseFlat <= widthLimit {
				// Base fits on a fresh line: move it there instead of
				// breaking the table list.
				l.br(level)
				l.lineOpen = true
				l.writeInline(base)
			} else {
				l.emitBrokenList(base, level)
			}
		}
		for _, js := range joins {
			l.emitJoinSeg(js, level)
		}

	case clInsert:
		l.openClause(level)
		l.writeInline(kw)
		l.emitInsertRest(rest, level)

	case clUpdate, clDelete, clHead:
		l.openClause(level)
		l.writeInline(kw)
		l.emitExpr(rest, level)

	case clWith:
		l.openClause(level)
		l.writeInline(kw)
		l.emitWithRest(rest, level)

	case clSetop, clRowLock:
		// D9: always on their own line.
		l.br(level)
		l.lineOpen = true
		l.writeInline(kw)
		l.emitExpr(rest, level)

	case clOnConflict:
		l.br(level)
		l.lineOpen = true
		l.writeInline(kw)
		l.emitExpr(rest, level)

	case clTail:
		l.openClause(level)
		fit := !hard && l.fitsAhead(level, flatAll, &stmt[seg.start])
		if !fit {
			l.br(level)
			l.lineOpen = true
		}
		l.writeInline(kw)
		if len(rest) == 0 {
			return
		}
		if fit {
			// LIMIT/OFFSET glue onto the current line whenever it fits.
			l.writeInline(rest)
			return
		}
		l.lineOpen = false
		l.br(level + 1)
		l.emitExpr(rest, level+1)
	}
}

// emitBrokenList emits comma-separated items one per line at level+1 with
// the source's own commas in trailing position (never added).
func (l *layouter) emitBrokenList(atoms []atom, level int) {
	items, seps := splitOn(atoms, isComma)
	l.lineOpen = false
	l.emitCommaItems(items, seps, level)
}

// emitCommaItems emits comma-separated items one per line at level+1,
// keeping the source's commas in trailing position (never added). A comma
// that would land on a comment line moves to the start of the next item
// line instead — a "--" comment would swallow it.
func (l *layouter) emitCommaItems(items [][]atom, seps []*atom, level int) {
	l.lineOpen = false
	deferred := false
	for ii, item := range items {
		l.br(level + 1)
		if deferred {
			l.writeRaw(",")
			l.last2A, l.lastA = nil, seps[ii-1]
			deferred = false
		}
		l.emitExpr(item, level+1)
		if ii < len(seps) {
			if l.curHasTok && l.lastA != nil && l.lastA.kind == atomComment {
				deferred = true
			} else {
				l.writeRaw(",")
			}
		}
	}
}

// splitJoinSegs splits FROM rest into JOIN segments (each starting at a
// JOIN unit; ON/USING and the join condition stay inside the segment).
func splitJoinSegs(rest []atom) [][]atom {
	var segs [][]atom
	start := -1
	i := 0
	for i < len(rest) {
		a := &rest[i]
		if a.kind == atomComment {
			i++
			continue
		}
		if a.ttype == models.TokenTypeLParen {
			j := matchParen(rest, i)
			if j < 0 {
				i++
				continue
			}
			i = j + 1
			continue
		}
		if keyword(a) == "CASE" {
			j := matchCase(rest, i)
			if j < 0 {
				i++
				continue
			}
			i = j + 1
			continue
		}
		if isJoinUnitStart(rest, i) {
			// A bare modifier (LEFT) directly followed by its JOIN token
			// ("OUTER JOIN" merged or plain JOIN) is ONE unit: no split.
			if start >= 0 && i == start+1 && isBareModifier(&rest[start]) {
				i++
				continue
			}
			if start >= 0 {
				segs = append(segs, rest[start:i])
			}
			start = i
		}
		i++
	}
	if start >= 0 {
		segs = append(segs, rest[start:])
	}
	return segs
}

// joinBaseEnd returns the end index of the comma-table list before the
// first JOIN segment.
func joinBaseEnd(rest []atom) int {
	i := 0
	for i < len(rest) {
		a := &rest[i]
		if a.kind == atomComment {
			i++
			continue
		}
		if a.ttype == models.TokenTypeLParen {
			j := matchParen(rest, i)
			if j < 0 {
				i++
				continue
			}
			i = j + 1
			continue
		}
		if isJoinUnitStart(rest, i) {
			return i
		}
		i++
	}
	return len(rest)
}

// splitJoinOn splits a JOIN segment at its top-level ON/USING keyword.
func splitJoinOn(js []atom) ([]atom, []atom, bool) {
	i := 0
	for i < len(js) {
		a := &js[i]
		if a.kind == atomComment {
			i++
			continue
		}
		if a.ttype == models.TokenTypeLParen {
			j := matchParen(js, i)
			if j < 0 {
				i++
				continue
			}
			i = j + 1
			continue
		}
		if keyword(a) == "CASE" {
			j := matchCase(js, i)
			if j < 0 {
				i++
				continue
			}
			i = j + 1
			continue
		}
		kw := keyword(a)
		if kw == "ON" || kw == "USING" {
			return js[:i], js[i:], true
		}
		i++
	}
	return js, nil, false
}

// emitJoinSeg lays out one JOIN segment: glued on the current line when
// it fits and has no top-level AND/OR in ON; otherwise the whole segment
// (or JOIN line + ON group) starts its own line, with the ON conditions
// at level+1 when AND/OR is present (D5).
func (l *layouter) emitJoinSeg(js []atom, level int) {
	jsHard := l.groupHard(js)
	jsFlat := l.flatLen(js)
	pre, on, hasOn := splitJoinOn(js)
	noAnd, condHard := true, false
	var conds [][]atom
	var seps []*atom
	if hasOn {
		cond := on[1:]
		conds, seps = splitOn(cond, isAndOr)
		noAnd = len(conds) <= 1
		condHard = l.groupHard(cond)
	}
	segFits := !jsHard && !condHard && noAnd && fitsFresh(l, level, jsFlat)
	if segFits {
		// Whole segment (JOIN t ON ...) inline on its own line.
		l.br(level)
		l.lineOpen = true
		l.writeInline(js)
		return
	}
	l.br(level)
	if !hasOn {
		l.writeInline(js)
		l.lineOpen = true
		return
	}
	l.lineOpen = true
	l.writeInline(pre)
	// D5: the broken ON group starts its own line at level+1 with the
	// first condition riding; AND/OR at line start.
	l.br(level + 1)
	l.writeInline(on[:1])
	if len(conds) <= 1 {
		l.emitExpr(on[1:], level+1)
		return
	}
	l.emitExpr(conds[0], level+1)
	for i := 1; i < len(conds); i++ {
		l.br(level + 1)
		l.writeAtom(seps[i-1])
		l.emitExpr(conds[i], level+1)
	}
}

// emitInsertRest lays out INSERT INTO <table> plus the optional column
// list frame: multiline column lists are args-preserved (source line
// structure re-indented to level+1); single-line lists follow the width
// and count rule and break with "(" terminating its line (D6).
func (l *layouter) emitInsertRest(rest []atom, level int) {
	fs, fe, ok := firstTopFrame(rest)
	if !ok {
		l.emitExpr(rest, level)
		return
	}
	table := rest[:fs]
	frame := rest[fs:fe]
	tail := rest[fe:]
	l.emitExpr(table, level)
	lp, rp := &frame[0], &frame[len(frame)-1]
	inner := frame[1 : len(frame)-1]
	frameHard := l.groupHard(frame)
	srcML := strings.Contains(l.src[lp.end:rp.start], "\n")
	if srcML && frameKindOf(inner) == frameArgs {
		// args-preserve: source line structure kept, re-indented.
		l.writeAtom(lp)
		l.lineOpen = false
		l.emitArgsLines(lp, rp, inner, level)
	} else {
		items, _ := splitOn(inner, isComma)
		fit := !frameHard && len(items) <= 4 && l.fitsAhead(level, l.flatLen(frame), lp)
		if fit {
			l.writeInline(frame)
		} else {
			l.lineOpen = false
			l.writeAtom(lp)
			items, seps := splitOn(inner, isComma)
			l.emitCommaItems(items, seps, level)
			l.br(level)
			l.writeAtom(rp)
			// The ")" line is extendable: ") VALUES (...)" may glue.
			l.lineOpen = true
		}
	}
	if len(tail) > 0 {
		l.emitExpr(tail, level)
	}
}

// firstTopFrame returns the atom index range of the first top-level
// paren frame in atoms.
func firstTopFrame(atoms []atom) (int, int, bool) {
	i := 0
	for i < len(atoms) {
		a := &atoms[i]
		if a.kind == atomComment {
			i++
			continue
		}
		if a.ttype == models.TokenTypeLParen {
			j := matchParen(atoms, i)
			if j < 0 {
				i++
				continue
			}
			return i, j + 1, true
		}
		i++
	}
	return 0, 0, false
}

// emitWithRest lays out the CTE definition list: each "name AS (...)"
// per def, ")" at the WITH level with the source comma glued after it,
// and a blank line between definitions (D8).
func (l *layouter) emitWithRest(rest []atom, level int) {
	defs, _ := splitOn(rest, isComma)
	for di, def := range defs {
		if di > 0 {
			l.br(level)
		}
		prefix, fr, tail := splitCteDef(def)
		l.writeInline(prefix)
		if fr != nil {
			l.emitFrame(fr, level)
		}
		if len(tail) > 0 {
			l.emitExpr(tail, level)
		}
		if di < len(defs)-1 {
			l.writeRaw(",")
			l.blankLine()
		}
	}
}

// splitCteDef splits one CTE definition into the "name ... AS" prefix,
// the subquery frame, and any tail atoms.
func splitCteDef(def []atom) (prefix []atom, frame []atom, tail []atom) {
	i := 0
	sawAS := false
	for i < len(def) {
		a := &def[i]
		if a.kind == atomComment {
			i++
			continue
		}
		if a.ttype == models.TokenTypeLParen {
			j := matchParen(def, i)
			if j < 0 {
				i++
				continue
			}
			if sawAS {
				return def[:i], def[i : j+1], def[j+1:]
			}
			// column list before AS: part of the prefix
			i = j + 1
			continue
		}
		if keyword(a) == "AS" {
			sawAS = true
		}
		i++
	}
	return def, nil, nil
}

// emitExpr emits an expression range: inline when it fits on the current
// line, otherwise structurally (frames and CASE laid out by their own
// rules, comments on their own lines).
func (l *layouter) emitExpr(atoms []atom, level int) {
	if len(atoms) == 0 {
		return
	}
	flat, hard := l.flatHard(atoms)
	if !hard && l.fitsAhead(level, flat, &atoms[0]) {
		l.writeInline(atoms)
		return
	}
	l.lineOpen = false
	i := 0
	for i < len(atoms) {
		a := &atoms[i]
		switch {
		case a.kind == atomComment:
			l.br(level)
			l.writeAtom(a)
		case a.ttype == models.TokenTypeLParen:
			j := matchParen(atoms, i)
			if j < 0 {
				l.writeAtom(a)
				i++
				continue
			}
			l.emitFrame(atoms[i:j+1], level)
			i = j + 1
			continue
		case keyword(a) == "CASE":
			j := matchCase(atoms, i)
			if j < 0 {
				l.writeAtom(a)
				i++
				continue
			}
			l.emitCase(atoms[i:j+1], level)
			i = j + 1
			continue
		default:
			l.writeAtom(a)
		}
		i++
	}
}

// emitFrame lays out one paren frame: inline when it fits; a broken
// subquery frame gets "(" at the end of the current line, its content at
// level+1 with full clause rules, and ")" on its own line at level (D6);
// a broken multiline args frame keeps its source line structure.
func (l *layouter) emitFrame(f []atom, level int) {
	lp, rp := &f[0], &f[len(f)-1]
	inner := f[1 : len(f)-1]
	flat, hard := l.flatHard(f)
	if !hard && l.fitsAhead(level, flat, lp) {
		l.writeInline(f)
		return
	}
	l.lineOpen = false
	l.writeAtom(lp)
	if frameKindOf(inner) == frameSubquery {
		// "(" terminates its line: the frame content starts fresh (D6).
		l.pendingBreak = true
		l.emitBlock(inner, level+1)
		l.br(level)
		l.writeAtom(rp)
		return
	}
	if strings.Contains(l.src[lp.end:rp.start], "\n") {
		l.emitArgsLines(lp, rp, inner, level)
		l.lineOpen = true
		return
	}
	// Single-line args frame that does not fit: stay inline (documented
	// limit — breaking would lose the author's padding).
	for i := 1; i < len(f); i++ {
		l.writeAtom(&f[i])
	}
}

// emitArgsLines re-emits a multiline args frame's source lines: the line
// structure and internal padding are kept, the common indent is replaced
// by level+1 units, and ")" lands on its own line at level (D6).
// Keyword-class atoms inside the frame region carry their (whitelist-
// normalized) atom text via inner (the atoms between lp and rp).
func (l *layouter) emitArgsLines(lp, rp *atom, inner []atom, level int) {
	lines := strings.Split(upperRawRegion(l.src, lp.end, rp.start, inner), "\n")
	start := 0
	if strings.TrimSpace(lines[0]) == "" && len(lines) > 1 {
		start = 1 // content begins on the line after "("
	}
	common := commonIndentLines(lines[start:])
	// Drop blank source lines (D8: the formatter introduces blank lines
	// only between statements and CTE definitions).
	end := len(lines)
	if end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	for i := start; i < end; i++ {
		ln := lines[i]
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if i == start && start == 0 {
			// content glued on the "(" line
			l.writeRaw(strings.TrimLeft(ln, " \t"))
			continue
		}
		l.br(level + 1)
		l.writeRaw(strings.TrimPrefix(ln, common))
	}
	l.br(level)
	l.writeAtom(rp)
	// The ")" line is extendable: ") VALUES (...)" may glue.
	l.lineOpen = true
}

// upperRawRegion re-emits src[from:to] with each keyword-class atom's raw
// slice replaced by its whitelist-normalized atom text (upperKeyword was
// already applied in atomize); comments and inter-atom gaps keep their
// raw source bytes.
func upperRawRegion(src string, from, to int, atoms []atom) string {
	var b strings.Builder
	b.Grow(to - from)
	prev := from
	for i := range atoms {
		a := &atoms[i]
		if a.start < from || a.end > to || !isKeywordClass(a) {
			continue
		}
		b.WriteString(src[prev:a.start])
		b.WriteString(a.text)
		prev = a.end
	}
	b.WriteString(src[prev:to])
	return b.String()
}

// emitCase lays out a CASE..END range: inline when it fits; otherwise
// CASE at the current position, each WHEN <cond> THEN <result> on one
// line at level+1 when it fits (a too-wide WHEN breaks at its ANDs,
// which land at line start at CASE level+1), and END at the CASE's own
// level (D7).
func (l *layouter) emitCase(crange []atom, level int) {
	flat, hard := l.flatHard(crange)
	if !hard && l.fitsAhead(level, flat, &crange[0]) {
		l.writeInline(crange)
		return
	}
	l.lineOpen = false
	l.writeAtom(&crange[0]) // CASE glues to the current content
	body := crange[1 : len(crange)-1]
	segs, seps := splitOn(body, func(a *atom) bool {
		kw := keyword(a)
		return kw == "WHEN" || kw == "ELSE"
	})
	// seps[i] is the WHEN/ELSE atom introducing segs[i+1]; segs[0] holds
	// any atoms before the first WHEN (empty for well-formed CASE).
	for si, seg := range segs {
		if si == 0 {
			if len(seg) > 0 {
				l.br(level + 1)
				l.emitExpr(seg, level+1)
			}
			continue
		}
		intro := seps[si-1]
		introW := utf8.RuneCountInString(intro.text)
		if keyword(intro) == "ELSE" {
			brFlat, brHard := l.flatHard(seg)
			l.br(level + 1)
			l.writeAtom(intro)
			if !brHard && fitsFresh(l, level+1, introW+1+brFlat) {
				l.writeInline(seg)
			} else {
				l.emitExpr(seg, level+1)
			}
			continue
		}
		// WHEN branch: one line when the whole WHEN..THEN fits.
		whenFlat, whenHard := l.flatHard(seg)
		if !whenHard && fitsFresh(l, level+1, introW+1+whenFlat) {
			l.br(level + 1)
			l.writeAtom(intro) // WHEN
			l.writeInline(seg)
			continue
		}
		// Too wide: WHEN + conjuncts at line start, THEN on its own line.
		conds, cseps := splitOn(seg, func(a *atom) bool { return keyword(a) == "THEN" })
		condPart := seg
		var thenKw *atom
		var thenPart []atom
		if len(conds) > 1 {
			condPart = conds[0]
			thenKw = cseps[0]
			thenPart = conds[1]
		}
		juncts, jseps := splitOn(condPart, isAndOr)
		l.br(level + 1)
		l.writeAtom(intro) // WHEN
		l.emitExpr(juncts[0], level+1)
		for i := 1; i < len(juncts); i++ {
			l.br(level + 1)
			l.writeAtom(jseps[i-1])
			l.emitExpr(juncts[i], level+1)
		}
		if thenKw != nil {
			l.br(level + 1)
			l.writeAtom(thenKw)
			l.emitExpr(thenPart, level+1)
		}
	}
	l.br(level)
	l.writeAtom(&crange[len(crange)-1]) // END at the CASE's own level
}

// fitsFresh reports whether flat runes fit on a fresh line at level.
func fitsFresh(l *layouter, level, flat int) bool {
	return level*l.unitW+flat <= widthLimit
}

// commonIndentLines returns the common leading whitespace of the non-blank
// lines in the slice.
func commonIndentLines(lines []string) string {
	indent := ""
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		run := leadingSpaceTab(l)
		if indent == "" {
			indent = run
			continue
		}
		indent = commonPrefix(indent, run)
		if indent == "" {
			return ""
		}
	}
	return indent
}
