package corpus

// 003 — where chains: AND/OR at line start, BETWEEN, operator padding.
const q1 = `SELECT r.raceid, r.name, r.date FROM races r
	WHERE r.date >= $1
		AND r.date < $1 + INTERVAL '1 year'
		AND r.position IS NULL`

const q2 = `SELECT id FROM orders
	WHERE created_at BETWEEN $1 AND $2
		AND status = 'paid'
		AND total > 0`

const q3 = `SELECT * FROM t
	WHERE a = 1
		AND (b = 2 OR c = 3)
		AND d IS NOT NULL`

const q4 = `SELECT raceid, position FROM results
	WHERE raceid IN (SELECT raceid FROM races WHERE year = 2021)
		AND position <= 3`
