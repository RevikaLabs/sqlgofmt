package corpus

// 005 — CTEs: several definitions, blank line between, nested subquery.
const q1 = `WITH sample AS (SELECT id, name FROM users WHERE active),

	filtered AS (SELECT id FROM sample WHERE name LIKE 'A%') SELECT * FROM filtered`

const q2 = `WITH region_sales AS (
		SELECT region, SUM(amount) AS total FROM sales GROUP BY region
	),

	top AS (SELECT region FROM region_sales ORDER BY total DESC LIMIT 5) SELECT s.*
	FROM sales s JOIN top ON top.region = s.region`

const q3 = `WITH RECURSIVE tree AS (
		SELECT id, parent_id, 1 AS depth FROM nodes WHERE parent_id IS NULL
		UNION ALL SELECT n.id, n.parent_id, t.depth + 1 FROM nodes n
		JOIN tree t ON n.parent_id = t.id
	)
	SELECT * FROM tree`
