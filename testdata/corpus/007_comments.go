package corpus

// 007 — comments: leading own-line, trailing aligned, block comments.
const q1 = `-- fetch all active workspaces with their plan
	-- ordered by creation date
	SELECT w.id, w.name, w.plan -- plan is the billing tier
	FROM workspaces w
	WHERE w.deleted_at IS NULL
		AND w.active = TRUE -- only active
	ORDER BY w.created_at`

const q2 = `SELECT     /* streaming replicas only */ host, port
	FROM nodes /* skip analytics nodes */ WHERE kind = 'stream'`

const q3 = `/*
	 * Generate the target month's calendar, then LEFT JOIN each day
	 * against the factbook dataset.
	 */
	WITH days AS (
		SELECT
			generate_series($1::date, $1::date + INTERVAL '1 month - 1 day', INTERVAL '1 day')::date AS day
	)
	SELECT d.day, f.event FROM days d LEFT JOIN factbook f ON f.day = d.day`

const q4 = `SELECT
		id,
		name,  -- display name
		email, -- login handle
		role   -- rbac role
	FROM admins`
