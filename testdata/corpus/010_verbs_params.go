package corpus

// 010 — format verbs, params, casts, dollar-quoted strings.
const q1 = `SELECT name FROM users
	WHERE name LIKE %s
		AND org_id = %d
	ORDER BY %s`

const q2 = `SELECT
		E.meta #>> '{live,type}' AS event_type,
		'a''b'::UUID AS uid,
		$1::timestamptz AS since
	FROM events WHERE payload @> $2`

const q3 = `SELECT id, def FROM funcs
	WHERE body LIKE $$LANGUAGE plpgsql%$$
		AND ns = %v`

const q4 = `SELECT * FROM t
	WHERE a = $1
		AND b = $2
		AND c = $3
		AND d = $4
		AND e = $5`
