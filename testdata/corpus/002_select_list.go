package corpus

// 002 — long select lists broken one per line with trailing commas.
const q1 = `SELECT
		entity_id,
		short_id,
		workspace_id,
		media_id,
		privacy_id,
		storage_id,
		subtitles_enabled,
		duration,
		status,
		meta #>> '{live,type}' AS event_type
	FROM entities`

const q2 = `SELECT
		u.id,
		u.email,
		u.full_name,
		u.avatar_url,
		u.role,
		u.created_at,
		u.last_seen_at
	FROM users u WHERE u.org_id = $1 ORDER BY u.created_at DESC LIMIT 50`

const q3 = `SELECT
		sum(points) FILTER (WHERE milliseconds IS NULL) AS dnfs,
		percentile_cont(ARRAY[0.5, 0.99]) WITHIN GROUP (ORDER BY cts - ats) AS parr,
		count(*)
	FROM results GROUP BY driverid`
