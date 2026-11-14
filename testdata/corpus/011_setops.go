package corpus

// 011 — set operations, group/having/order.
const q1 = `SELECT driverid FROM results WHERE position = 1
	INTERSECT SELECT driverid FROM results WHERE position = 2 ORDER BY driverid`

const q2 = `SELECT name FROM runners_2020
	UNION ALL SELECT name FROM runners_2021 ORDER BY name`

const q3 = `SELECT status, count(*) FROM jobs GROUP BY status HAVING count(*) > 10
	ORDER BY
		count(*) DESC`

const q4 = `SELECT driverid FROM podium_2020
	EXCEPT SELECT driverid FROM podium_2021`

const q5 = `SELECT region, product, SUM(amount) FROM sales GROUP BY region, product
	HAVING SUM(amount) > 1000
	ORDER BY region, product`
