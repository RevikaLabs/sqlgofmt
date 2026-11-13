# sqlgofmt

`sqlgofmt` formats SQL inside raw string literals (backticks) of Go source
files, powered by [GoSQLX](https://github.com/ajitpratap0/GoSQLX).

It detects raw string literals that look like SQL, formats them, re-applies
the literal's indentation, and rewrites the file in place (byte-level edits
— the rest of the file is never touched). Formatting is idempotent: running
sqlgofmt twice produces the same result.

## Install

```sh
go install github.com/RevikaLabs/sqlgofmt/cmd/sqlgofmt@latest
```

## Usage

```sh
sqlgofmt [flags] <path...>          # print formatted files to stdout
sqlgofmt -w ./internal/db           # rewrite files in place
sqlgofmt -l ./...                   # list files that need formatting (exit 1)
sqlgofmt -d main.go                 # print unified diffs
sqlgofmt -dialect mysql -w db.go    # format with a specific dialect
```

Flags:

| Flag       | Default     | Meaning                                          |
|------------|-------------|--------------------------------------------------|
| `-w`       | `false`     | write result to the source file                  |
| `-l`       | `false`     | list files whose formatting differs (exit code 1)|
| `-d`       | `false`     | print diffs instead of formatted output          |
| `-dialect` | `postgres`  | `postgres`/`postgresql`, `mysql`, `sqlserver`/`mssql`, `oracle`, `clickhouse` |
| `-debug`   | `false`     | print reasons for skipped literals to stderr     |

Paths may be `.go` files or directories (walked recursively). The
directories `vendor`, `.git`, `testdata` and dot-dirs are skipped.

## Skip rules

A raw string literal is formatted only when **all** of the following hold:

- it is not a struct tag;
- it is not an operand of a string concatenation (`+`);
- it contains no `{{` or `\r` (Go templates / CR line endings). Format
  verbs (`%s`, `%10s`, `%%`, …) are **not** skipped: the formatter masks
  them for parsing and emits them byte-exact;
- it contains no `::::` run of colons (sqlx escapes named parameters by
  doubling colons — such literals are skipped);
- it contains no nested block comment (`/* /* */ */`): PostgreSQL nests
  block comments, the tokenizer does not, so the nested comment's tail
  would be re-tokenized as SQL code (`ReasonNestedComment`). The check
  runs for every dialect — MySQL/ClickHouse block comments do not nest,
  so such literals may be skipped unnecessarily, which is the safe
  direction;
- it is not preceded (same or previous line) by a `//sqlgofmt:ignore` comment;
- it starts (after trimming, case-insensitive) with a DML/procedural SQL
  keyword: `SELECT, INSERT, UPDATE, DELETE, WITH, BEGIN, COMMIT, ROLLBACK,
  REPLACE, MERGE, CALL, SET, DO`;
- it tokenizes and parses cleanly with GoSQLX (under the `-dialect`
  dialect), and the layout is stable on its own output (round-trip check).

Literals that fail any rule are left untouched. **DDL is intentionally not
formatted**: statements starting with `CREATE, ALTER, DROP, GRANT, REVOKE,
TRUNCATE, COMMENT ON, VACUUM, ANALYZE, EXPLAIN, LOCK, COPY` are always left
as written (too many dialect shapes for a token-based engine to re-layout).

Single-line literals **are** formatted (v4): a literal that entirely fits
on one line stays one line; when it must break, the literal becomes
multiline and the base indent is derived from the Go indentation of the
backtick's line (plus one level), so no SQL line lands at column 0.

## Formatting style

sqlgofmt follows its own style, synthesized from
[sqlstyle.guide](https://sqlstyle.guide), Mozilla's data-docs SQL style
guide and dbt Labs conventions: left-aligned keywords, tab indent, trailing
commas, AND/OR at line start, width-80/≤4-items breaking, blank lines
between statements and CTEs. Whitespace, comma position in broken lists
and keyword casing are the only things that ever change — keywords are
normalized to UPPER CASE against a curated clause-keyword whitelist
(keyword-classified words off the whitelist, e.g. a column named `hash`,
and qualified names like `sec.key` keep the author's casing; string
literals and quoted identifiers are never touched) — and no token is ever
added or dropped.
Explicit `AS` or CTEs instead of nested queries remain the author's job
(authoring rules are NOT enforced).

Breaking is decided top-down per clause group, from flat width and item
count only — never from source newlines: a list (SELECT items, SET
assignments, RETURNING, GROUP BY, ORDER BY, VALUES tuples, ON conditions)
stays inline if it fits in 80 columns at its level **and** has at most 4
top-level items; otherwise the keyword sits alone and items go one per line
at the next level with trailing commas. A comment or a multiline
function-argument frame inside a group always breaks it.

One documented exception to trailing commas: when an item is followed by a
`--` comment, the comma moves to the start of the next item's line (a
trailing comma after a `--` comment would be swallowed by it).

- root keywords are left-aligned at level 0 (one tab per level inside
  subqueries); clause keywords glue onto the current line only while it
  still fits — once something breaks, every following clause starts its
  own line;
- keywords are normalized to UPPER CASE only for a curated clause-keyword
  whitelist (`SELECT`, `FROM`, `WHERE`, `JOIN`, `CASE`, `VALUES`, ...);
  keyword-classified words that are not on the whitelist (e.g. a column
  named `hash` or `key`) and qualified names like `sec.key` keep the
  author's casing, as do identifiers, quoted identifiers, string literals
  and comments;
- `AND`/`OR` always start their own line at the content level of their
  clause; the first condition of a `WHERE`/`HAVING` chain rides the
  keyword line when it fits after the keyword (each following `AND`/`OR`
  starts its own line even when it would fit). `AND` right after
  `BETWEEN` stays inline;
- `JOIN <table> <alias>` glues onto the current line when it fits;
  otherwise it gets its own line, and `ON` breaks to the next level with
  its conditions (an `AND`/`OR` inside `ON` always forces the break).
  Glue applies to at most 3 join segments: with 4+ joins each `JOIN` gets
  its own line even when everything would fit;
- a subquery frame that fits stays inline; when it breaks, `(` terminates
  its line (`WITH sample AS (`, `AS x (`), the content follows full clause
  rules one level deeper, and `)` sits alone at the `(` line's level;
- multiline function-argument frames (e.g. `json_build_object(...)`) keep
  their source line structure and alignment padding, re-indented one level
  deeper; a multiline INSERT column list keeps its source line structure;
- `CASE`: `WHEN <cond> THEN <result>` on one line per WHEN when the
  WHEN-line fits; a too-wide WHEN breaks at its `AND`s (line start at the
  CASE level + 1), `THEN` on its own line; `END` sits at the CASE's level;
- `UNION [ALL]`, `EXCEPT`, `INTERSECT`, `FOR UPDATE [SKIP LOCKED]` and
  `ON CONFLICT (...) DO NOTHING / DO UPDATE [SET ...]` always sit on their
  own line (the `DO` action glued to the conflict target);
- blank lines appear only between statements (after `;`) and between CTE
  definitions (after each `name AS (...),`);
- comments always sit on their own line, at the indent of their slot;
- between a bare identifier and `(` the source spacing is preserved (a
  table name and a function call are indistinguishable without parsing);
- the literal's original indentation is re-applied to every formatted line
  (the indent unit is a tab; library callers can configure it via
  `Options.Unit`).

Example:

```go
// before
const q = `SELECT u.id, u.email from users u
  left join orders o on o.user_id = u.id
WHERE u.active = true AND u.org_id = $1 ORDER BY u.email LIMIT 10`

// after
const q = `SELECT u.id, u.email FROM users u LEFT JOIN orders o ON o.user_id = u.id
	WHERE u.active = TRUE
		AND u.org_id = $1
	ORDER BY u.email LIMIT 10`
```

## Opt-out

To exclude a single literal, put a comment with `sqlgofmt:ignore` on the
same line or directly above it:

```go
//sqlgofmt:ignore
const q = `
select * from t`
```
