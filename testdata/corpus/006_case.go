package corpus

// 006 — CASE expressions: inline short, multiline with THEN/ELSE.
const q1 = `SELECT
		name,
		CASE WHEN name = 'France' THEN '#F2EFE9' ELSE '#ECECEC' END AS color
	FROM countries`

const q2 = `UPDATE user_stats SET rts = CASE WHEN action = 'rt' THEN rts + 1 ELSE rts END
	WHERE user_id = $1`

const q3 = `SELECT
		CASE
			WHEN score >= 90 THEN 'A'
			WHEN score >= 80 THEN 'B'
			WHEN score >= 70 THEN 'C'
			ELSE 'F'
		END AS grade,
		student_id
	FROM exams`
