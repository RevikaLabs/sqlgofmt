package corpus

// 004 — joins: inline single join, multi-join broken, ON with several conditions.
const q1 = `SELECT u.id, u.email FROM users u LEFT JOIN orders o ON o.user_id = u.id
	WHERE u.active = TRUE
		AND u.org_id = $1
	ORDER BY u.email LIMIT 10`

const q2 = `SELECT r.raceid, d.forename, d.surname, res.position FROM results res
	JOIN races r ON r.raceid = res.raceid
	JOIN drivers d ON d.driverid = res.driverid WHERE res.position = 1`

const q3 = `SELECT r.date, res.points FROM results res
	LEFT JOIN races r
		ON r.raceid = res.raceid
		AND r.year > 2010
		AND res.position <= 3 WHERE res.driverid = $1`

const q4 = `SELECT a.id, b.id FROM artists a JOIN bands b USING(bandid)`
