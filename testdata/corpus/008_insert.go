package corpus

// 008 — insert/values/on conflict.
const q1 = `INSERT INTO app.cleanup_queue (
		entry_id,
		data_class,
		payload,
		not_before,
		enqueued_at
	) VALUES ($1, $2, $3, $4, $4)
	ON CONFLICT (entry_id) DO NOTHING`

const q2 = `INSERT INTO audit_log (actor_id, action, target, created_at) VALUES (
		$1, $2, $3, now()
	) RETURNING id`

const q3 = `INSERT INTO counters (key, value, updated_at) VALUES ($1, $2, now())
	ON CONFLICT (key) DO UPDATE
	SET
		value = counters.value + EXCLUDED.value,
		updated_at = now()`

const q4 = `INSERT INTO events (workspace_id, kind, payload) VALUES (
		$1, 'import.finished', $2::jsonb
	)`
