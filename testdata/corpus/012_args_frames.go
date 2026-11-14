package corpus

// 012 — multiline args frames preserved with source alignment.
const q1 = `SELECT
		json_build_object(
			'cdn',           W.cdn,
			'features',      W.features,
			'hls_enabled',   W.hls_enabled,
			'blocked',       (W.blocked_at IS NOT NULL)
		) AS workspace FROM workspaces W WHERE W.id = $1`

const q2 = `SELECT coalesce(array_to_json(array_agg(row_to_json(s))), '[]') FROM
		(
			SELECT subtitle_id AS id, description, language FROM subtitles
			WHERE media_id = $1
				AND deleted_at IS NULL
		) s`
