package corpus

// 014 — adjacent string literals: PostgreSQL concatenates them only when
// separated by pure whitespace containing a newline (a comment between
// the literals invalidates the query entirely); the layout keeps them on
// different lines (pinned by the pg_query oracle).
const q1 = `SELECT
		'greeting'
		'world' AS composed
	FROM t`

const q2 = `SELECT
		'a'
		'b'
		'c' AS three
	FROM t`
