package corpus

// 001 — basic selects: trivial inline, few columns, LIMIT/OFFSET.
const q1 = `SELECT * FROM races LIMIT 1`

const q2 = `SELECT code, format('%s %s', forename, surname) AS fullname FROM drivers
	WHERE nationality = 'german'
	ORDER BY surname`

const q3 = `SELECT id, name, created_at FROM app.users u WHERE u.deleted_at IS NULL
	ORDER BY
		u.created_at DESC
	LIMIT 10 OFFSET 20`

const q4 = `SELECT 1`

const q5 = `SELECT count(*) AS n, min(created_at), max(updated_at) FROM events
	WHERE workspace_id = $1`
