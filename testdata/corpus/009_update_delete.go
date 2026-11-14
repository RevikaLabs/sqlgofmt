package corpus

// 009 — update/delete/returning.
const q1 = `UPDATE users SET email = $2, updated_at = now() WHERE id = $1
	RETURNING
		email,
		updated_at`

const q2 = `UPDATE sessions SET expires_at = now() + INTERVAL '1 hour', refreshed_at = now()
	WHERE token = $1
		AND expires_at > now()`

const q3 = `DELETE FROM cleanup_queue WHERE not_before < now()
	RETURNING
		entry_id,
		data_class`

const q4 = `UPDATE subscriptions SET plan_id = $2, renewed_until = $3
	WHERE user_id = $1
		AND canceled_at IS NULL`
