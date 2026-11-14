package corpus

// 013 — window functions and subqueries.
const q1 = `SELECT id, rank() OVER (ORDER BY score DESC) AS rnk FROM scores`

const q2 = `SELECT
		ds.driverid,
		(
			SELECT count(*) + 1 FROM driverstandings inner_ds
			WHERE inner_ds.raceid = ds.raceid
		) AS rank
	FROM driverstandings ds`

const q3 = `SELECT name, row_number() OVER (PARTITION BY dept ORDER BY salary DESC)
	FROM employees WHERE dept IS NOT NULL`

const q4 = `SELECT * FROM (SELECT id, sum(amount) AS total FROM payments GROUP BY id) p
	WHERE p.total > 1000`
