package processor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ajitpratap0/GoSQLX/pkg/sql/keywords"
)

// complexSample is a synthetic query in the old leading-comma style,
// exercising nested subqueries, CASE, args frames, comments and format
// verbs. It is not a fixed point by construction; the tests require
// stability, parseability and unchanged token text (G12).
const complexSample = "SELECT\n" +
	"\tE.entity_id\n" +
	"\t, E.short_id\n" +
	"\t, E.workspace_id\n" +
	"\t, M.media_id\n" +
	"\t, M.privacy_id\n" +
	"\t, M.storage_id\n" +
	"\t, M.subtitles_enabled\n" +
	"\t, M.duration\n" +
	"\t, M.status\n" +
	"\t, E.meta #>> '{live,type}' AS event_type\n" +
	"\t, (\n" +
	"\t\tCASE\n" +
	"\t\tWHEN W.bill_children\n" +
	"\t\tTHEN\n" +
	"\t\t\t(SELECT entity_id FROM org.entity WHERE path = subpath(E.path, 0, 1))\n" +
	"\t\tELSE\n" +
	"\t\t\t'00000000-0000-0000-0000-000000000000'::UUID\n" +
	"\t\tEND\n" +
	"\t) AS project_id\n" +
	"\t, (\n" +
	"\t\tSELECT row_to_json(p)\n" +
	"\t\tFROM (\n" +
	"\t\t\tSELECT\n" +
	"\t\t\t\tK.key_id\n" +
	"\t\t\t\t, K.iv\n" +
	"\t\t\tFROM sec.key AS K\n" +
	"\t\t\tWHERE K.key_id = M.encrypt_key_id\n" +
	"\t\t\t\tAND K.deleted_at IS NULL\n" +
	"\t\t\t\tAND E.sec IS NOT NULL\n" +
	"\t\t) p\n" +
	"\t) AS encrypt_key\n" +
	"\t, E.sec\n" +
	"\t, json_build_object(\n" +
	"\t\t'cdn',           W.cdn,\n" +
	"\t\t'features',      W.features,\n" +
	"\t\t'hls_enabled',   W.hls_enabled,\n" +
	"\t\t'blocked',       (W.blocked_at IS NOT NULL)\n" +
	"\t) AS workspace\n" +
	"\t, (\n" +
	"\t\tSELECT COALESCE(array_to_json(array_agg(S2.*)), '[]')\n" +
	"\t\tFROM (\n" +
	"\t\t\tSELECT\n" +
	"\t\t\t\tS.subtitle_id AS id\n" +
	"\t\t\t\t, S.description\n" +
	"\t\t\t\t, S.language\n" +
	"\t\t\t\t, TO_CHAR(COALESCE(S.updated_at, S.created_at), 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') AS updated_at\n" +
	"\t\t\tFROM content.subtitle AS S\n" +
	"\t\t\tWHERE M.media_id = S.media_id\n" +
	"\t\t\t\tAND S.deleted_at IS NULL\n" +
	"\t\t\t\tAND S.active IS TRUE\n" +
	"\t\t\t\tAND M.subtitles_enabled\n" +
	"\t\t\t\tAND S.status = 'done'\n" +
	"\t\t) AS S2\n" +
	"\t) AS subtitles\n" +
	"\t, ARRAY(\n" +
	"\t\tSELECT A.quality\n" +
	"\t\tFROM content.asset AS A\n" +
	"\t\tWHERE A.media_id = M.media_id\n" +
	"\t\t\tAND A.type = 'video'\n" +
	"\t\t\tAND A.deleted_at IS NULL\n" +
	"\t\tGROUP BY A.quality\n" +
	"\t\tHAVING bool_and(A.archived_at IS NOT NULL)\n" +
	"\t) AS archived_qualities\n" +
	"\t-- Страна плательщика: берётся из платящего проекта-родителя (тот же\n" +
	"\t-- CASE, что для project_id выше), если дети не платят сами.\n" +
	"\t, COALESCE((\n" +
	"\t\tSELECT a.country_code\n" +
	"\t\tFROM pay.account a\n" +
	"\t\tWHERE a.deleted_at IS NULL\n" +
	"\t\t\tAND a.workspace_id = (\n" +
	"\t\t\t\tCASE\n" +
	"\t\t\t\tWHEN W.bill_children\n" +
	"\t\t\t\tTHEN (SELECT PE.workspace_id FROM org.entity PE WHERE PE.path = subpath(E.path, 0, 1) LIMIT 1)\n" +
	"\t\t\t\tELSE W.workspace_id\n" +
	"\t\t\t\tEND\n" +
	"\t\t\t)\n" +
	"\t\tLIMIT 1\n" +
	"\t), 'unknown') AS billing_country\n" +
	"FROM org.entity AS E\n" +
	"JOIN core.org AS W USING(workspace_id)\n" +
	"JOIN content.media AS M ON M.media_id = E.media_id\n" +
	"WHERE %s\n" +
	"\tAND E.deleted_at IS NULL\n" +
	"\tAND M.deleted_at IS NULL"

// TestLayoutHouseSampleStable pins G12 for the legacy leading-comma
// fixture: Layout(x) is the v4 fixed point and token text is unchanged.
func TestLayoutHouseSampleStable(t *testing.T) {
	got, ok := Layout(complexSample, "")
	if !ok {
		t.Fatal("Layout rejected the house sample")
	}
	got2, ok := Layout(got, "")
	if !ok || got2 != got {
		t.Fatalf("house sample not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got, got2)
	}
	inSlices, outSlices := rawAtoms(complexSample), rawAtoms(got)
	if !sameTokenStreams(inSlices, outSlices) {
		t.Fatalf("token stream changed:\n--- in ---\n%v\n--- out ---\n%v", inSlices, outSlices)
	}
}

// guideCorpus is the sqlstyle.guide (Simon Holywell) example corpus. The
// corpus is "influenced-by", not byte-fixed: each input must parse, be
// idempotent under v4 and keep its token stream intact (G12).
var guideCorpus = []struct {
	name string
	in   string
}{
	{
		name: "I1",
		in:   "SELECT file_hash  -- stored ssdeep hash\n  FROM file_system\n WHERE file_name = '.vimrc';",
	},
	{
		name: "I2",
		in:   "UPDATE file_system\n   SET file_modified_date = '1980-02-22 13:19:01.00000',\n       file_size = 209732\n WHERE file_name = '.vimrc';",
	},
	{
		name: "I3",
		in:   "SELECT first_name AS fn\n  FROM staff AS s1\n  JOIN students AS s2\n    ON s2.mentor_id = s1.staff_num;",
	},
	{name: "I4", in: "SELECT SUM(s.monitor_tally) AS monitor_total\n  FROM staff AS s;"},
	{
		name: "I5",
		in:   "INSERT INTO albums (title, release_date, recording_date)\nVALUES ('Charcoal Lane', '1990-01-01 01:01:01.00000', '1990-01-01 01:01:01.00000'),\n       ('The New Danger', '2008-01-01 01:01:01.00000', '1990-01-01 01:01:01.00000');",
	},
	{
		name: "I6",
		in:   "UPDATE albums\n   SET release_date = '1990-01-01 01:01:01.00000'\n WHERE title = 'The New Danger';",
	},
	{
		name: "I7",
		in:   "SELECT a.title,\n       a.release_date, a.recording_date, a.production_date -- grouped dates together\n  FROM albums AS a\n WHERE a.title = 'Charcoal Lane'\n    OR a.title = 'The New Danger';",
	},
	{
		name: "I8",
		in:   "SELECT r.last_name\n  FROM riders AS r\n       INNER JOIN bikes AS b\n       ON r.bike_vin_num = b.vin_num\n          AND b.engine_tally > 2\n       INNER JOIN crew AS c\n       ON r.crew_chief_last_name = c.last_name\n          AND c.chief = 'Y';",
	},
	{
		name: "I9",
		in:   "SELECT r.last_name,\n       (SELECT MAX(YEAR(championship_date))\n          FROM champions AS c\n         WHERE c.last_name = r.last_name\n           AND c.confirmed = 'Y') AS last_championship_year\n  FROM riders AS r\n WHERE r.last_name IN\n       (SELECT c.last_name\n          FROM champions AS c\n         WHERE YEAR(championship_date) > '2008'\n           AND c.confirmed = 'Y');",
	},
	{
		name: "I10",
		in:   "SELECT CASE postcode\n       WHEN 'BN1' THEN 'Brighton'\n       WHEN 'EH1' THEN 'Edinburgh'\n       END AS city\n  FROM office_locations\n WHERE country = 'United Kingdom'\n   AND opening_time BETWEEN 8 AND 9\n   AND postcode IN ('EH1', 'BN1', 'NN1', 'KW1');",
	},
}

func TestLayoutGuideCorpus(t *testing.T) {
	for _, tc := range guideCorpus {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Layout(tc.in, "")
			if !ok {
				t.Fatalf("Layout rejected guide input %s", tc.name)
			}
			got2, ok := Layout(got, "")
			if !ok || got2 != got {
				t.Fatalf("guide %s not idempotent\n--- once ---\n%s\n--- twice ---\n%s", tc.name, got, got2)
			}
			inSlices, outSlices := rawAtoms(tc.in), rawAtoms(got)
			if !sameTokenStreams(inSlices, outSlices) {
				t.Fatalf("guide %s token stream changed", tc.name)
			}
			// Structural check: broken lists use trailing commas — no
			// line may start with a comma.
			for _, line := range strings.Split(got, "\n") {
				if strings.HasPrefix(strings.TrimLeft(line, "\t "), ",") {
					t.Fatalf("guide %s has a leading comma line (must be trailing):\n%s", tc.name, got)
				}
			}
		})
	}
}

// regressionCorpus pins real-world shapes in exact v4 style.
var regressionCorpus = []struct {
	name string
	in   string
	want string
}{
	{
		name: "R1_joins_where_order_limit",
		in:   "SELECT m.data_class, m.expires_at, m.bucket_id, b.bucket\nFROM app.object_manifest m\nJOIN app.bucket b ON (b.data_class, b.bucket_id) = (m.data_class, m.bucket_id)\nWHERE m.expires_at IS NOT NULL AND m.expires_at <= now()\nORDER BY m.data_class, m.expires_at, m.bucket_id, m.key LIMIT $1",
		want: "SELECT m.data_class, m.expires_at, m.bucket_id, b.bucket\n" +
			"FROM app.object_manifest m\n" +
			"JOIN app.bucket b ON (b.data_class, b.bucket_id) = (m.data_class, m.bucket_id)\n" +
			"WHERE m.expires_at IS NOT NULL\n" +
			"\tAND m.expires_at <= now()\n" +
			"ORDER BY m.data_class, m.expires_at, m.bucket_id, m.key LIMIT $1",
	},
	{
		name: "R2_on_conflict_do_nothing",
		in:   "INSERT INTO app.cleanup_queue (entry_id, data_class, payload, not_before, enqueued_at)\nVALUES ($1, $2, $3, $4, $4)\nON CONFLICT (entry_id) DO NOTHING",
		want: "INSERT INTO app.cleanup_queue (\n" +
			"\tentry_id,\n" +
			"\tdata_class,\n" +
			"\tpayload,\n" +
			"\tnot_before,\n" +
			"\tenqueued_at\n" +
			") VALUES ($1, $2, $3, $4, $4)\n" +
			"ON CONFLICT (entry_id) DO NOTHING",
	},
	{
		name: "R2a_union_all_parens",
		in:   "(SELECT id FROM a) UNION ALL (SELECT id FROM b)",
		want: "(SELECT id FROM a)\nUNION ALL (SELECT id FROM b)",
	},
	{
		name: "R2b_for_update_of_nowait",
		in:   "SELECT id FROM t WHERE x = $1\nFOR UPDATE OF t NOWAIT",
		want: "SELECT id FROM t WHERE x = $1\nFOR UPDATE OF t NOWAIT",
	},
	{
		name: "R3_select_list_and_for_update",
		in:   "SELECT c1,\n c2,\n c3,\n c4,\n c5,\n c6,\n c7\nFROM app.t\nWHERE k = $1\nLIMIT $2\nFOR UPDATE SKIP LOCKED",
		want: "SELECT\n" +
			"\tc1,\n" +
			"\tc2,\n" +
			"\tc3,\n" +
			"\tc4,\n" +
			"\tc5,\n" +
			"\tc6,\n" +
			"\tc7\n" +
			"FROM app.t WHERE k = $1 LIMIT $2\n" +
			"FOR UPDATE SKIP LOCKED",
	},
	{
		name: "R4_update_set_and_where_chain",
		in:   "UPDATE app.object_manifest\nSET data_class=$1,\n    expires_at=$2,\n    bucket_id=$3,\n    key=$4,\n    size=$5,\n    etag=$6,\n    owner=$7,\n    flags=$8,\n    version=$9,\n    state=$10,\n    note=$11,\n    extra=$12\nWHERE bucket_id = $13\nAND data_class = 'a'\nAND expires_at IS NOT NULL",
		want: "UPDATE app.object_manifest\n" +
			"SET\n" +
			"\tdata_class = $1,\n" +
			"\texpires_at = $2,\n" +
			"\tbucket_id = $3,\n" +
			"\tkey = $4,\n" +
			"\tsize = $5,\n" +
			"\tetag = $6,\n" +
			"\towner = $7,\n" +
			"\tflags = $8,\n" +
			"\tversion = $9,\n" +
			"\tstate = $10,\n" +
			"\tnote = $11,\n" +
			"\textra = $12\n" +
			"WHERE bucket_id = $13\n" +
			"\tAND data_class = 'a'\n" +
			"\tAND expires_at IS NOT NULL",
	},
	{
		name: "R5_delete_from_returning",
		in:   "DELETE FROM app.multipart_upload\nWHERE upload_id = $1\nRETURNING (SELECT b.bucket FROM app.bucket b WHERE b.bucket_id = multipart_upload.bucket_id) AS bucket,\n          (SELECT c.data_class FROM app.bucket c WHERE c.bucket_id = multipart_upload.bucket_id) AS data_class",
		want: "DELETE FROM app.multipart_upload WHERE upload_id = $1\n" +
			"RETURNING\n" +
			"\t(\n" +
			"\t\tSELECT b.bucket FROM app.bucket b\n" +
			"\t\tWHERE b.bucket_id = multipart_upload.bucket_id\n" +
			"\t) AS bucket,\n" +
			"\t(\n" +
			"\t\tSELECT c.data_class FROM app.bucket c\n" +
			"\t\tWHERE c.bucket_id = multipart_upload.bucket_id\n" +
			"\t) AS data_class",
	},
	{
		name: "R6_insert_multiline_column_list",
		in:   "INSERT INTO app.t (\n  a,\n  b\n)\nVALUES ($1, $2)",
		want: "INSERT INTO app.t (\n" +
			"\ta,\n" +
			"\tb\n" +
			") VALUES ($1, $2)",
	},
	{
		name: "R7_with_cte_insert",
		in:   "WITH s AS (\nINSERT INTO archive.t (id)\nVALUES ($1)\nRETURNING id\n)\nSELECT s.id FROM s JOIN other o ON o.id = s.id",
		want: "WITH s AS (INSERT INTO archive.t (id) VALUES ($1) RETURNING id) SELECT s.id\n" +
			"FROM s JOIN other o ON o.id = s.id",
	},
	{
		name: "R8_exists_multiline_subquery",
		in:   "SELECT EXISTS (\n  SELECT 1 FROM app.object_manifest\n  WHERE upload_id = $1 AND (expires_at IS NULL OR expires_at > now()))",
		want: "SELECT\n" +
			"\tEXISTS (\n" +
			"\t\tSELECT 1 FROM app.object_manifest\n" +
			"\t\tWHERE upload_id = $1\n" +
			"\t\t\tAND (expires_at IS NULL OR expires_at > now())\n" +
			"\t)",
	},
	{
		// Pins the revika audit/queries.go shape: multiline column list
		// and VALUES tuples keep source structure, "VALUES (" stays glued
		// to the ")" line, and author identifiers like hash keep casing.
		name: "R9_insert_values_args_frame_glue",
		in: "INSERT INTO audit.log (\n" +
			"    at, org_id, actor_id, prev_hash, hash\n" +
			") VALUES (\n" +
			"    $1, $2, $3,\n" +
			"    $4, $5\n" +
			")",
		want: "INSERT INTO audit.log (\n" +
			"\tat, org_id, actor_id, prev_hash, hash\n" +
			") VALUES (\n" +
			"\t$1, $2, $3,\n" +
			"\t$4, $5\n" +
			")",
	},
}

func TestLayoutRegression(t *testing.T) {
	for _, tc := range regressionCorpus {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Layout(tc.in, "")
			if !ok {
				t.Fatalf("Layout rejected regression input %s", tc.name)
			}
			if got != tc.want {
				t.Fatalf("regression %s mismatch\n--- got ---\n%s\n--- want ---\n%s", tc.name, got, tc.want)
			}
			got2, ok := Layout(got, "")
			if !ok || got2 != got {
				t.Fatalf("regression %s not idempotent", tc.name)
			}
		})
	}
}

// TestG1FitsInline pins G1: a statement whose groups all fit stays one
// line — no gratuitous clause-per-line breaking.
func TestG1FitsInline(t *testing.T) {
	in := "SELECT id, name FROM t WHERE x = 1"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected G1 input")
	}
	if got != in {
		t.Fatalf("G1 must stay one line\n got: %q\nwant: %q", got, in)
	}
	got2, ok := Layout(got, "")
	if !ok || got2 != got {
		t.Fatal("G1 not idempotent")
	}
}

// TestG2LongSelectBreaks pins G2: more than 4 top-level items always
// break, keyword alone, items one per line at level+1 with the source's
// own commas in trailing position.
func TestG2LongSelectBreaks(t *testing.T) {
	in := "SELECT alpha, beta, gamma, delta, epsilon, zeta FROM t"
	want := "SELECT\n" +
		"\talpha,\n" +
		"\tbeta,\n" +
		"\tgamma,\n" +
		"\tdelta,\n" +
		"\tepsilon,\n" +
		"\tzeta\n" +
		"FROM t"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected G2 input")
	}
	if got != want {
		t.Fatalf("G2 mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	got2, _ := Layout(got, "")
	if got2 != got {
		t.Fatal("G2 not idempotent")
	}
}

// TestG3WidthBoundary pins G3: at level 0 a SELECT clause whose printed
// line (keyword + items) is exactly 80 runes stays inline; 81 runes
// breaks the list. No flip on re-layout.
func TestG3WidthBoundary(t *testing.T) {
	items := []string{
		strings.Repeat("a", 17),
		strings.Repeat("b", 17),
		strings.Repeat("c", 17),
		strings.Repeat("d", 16),
	}
	// "SELECT " (7) + 17+2+17+2+17+2+16 (73) = 80.
	in80 := "SELECT " + strings.Join(items, ", ") + " FROM t"
	got, ok := Layout(in80, "")
	if !ok {
		t.Fatal("Layout rejected 80-boundary input")
	}
	if !strings.HasPrefix(got, in80[:80]) || strings.Count(got, "\n") != 1 {
		t.Fatalf("flat width exactly 80 must keep the select list inline, got:\n%s", got)
	}
	// One more rune in the last item: 81 -> the list breaks.
	in81 := "SELECT " + strings.Join(items, ", ") + strings.Repeat("e", 1) + " FROM t"
	got81, ok := Layout(in81, "")
	if !ok {
		t.Fatal("Layout rejected 81-boundary input")
	}
	if !strings.Contains(got81, "\n\tddddd") {
		t.Fatalf("flat width 81 must break the list, got:\n%s", got81)
	}
	// No flip on re-layout for either boundary.
	for name, in := range map[string]string{"80": got, "81": got81} {
		again, ok := Layout(in, "")
		if !ok || again != in {
			t.Fatalf("boundary %s not idempotent\n--- once ---\n%s\n--- twice ---\n%s", name, in, again)
		}
	}
}

// TestG4AndOr pins G4: AND present forces the WHERE chain to break; the
// first condition rides the WHERE line, AND starts a line at level+1.
func TestG4AndOr(t *testing.T) {
	in := "SELECT a FROM t WHERE x = $1 AND y = 2"
	want := "SELECT a FROM t\n" +
		"WHERE x = $1\n" +
		"\tAND y = 2"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected G4 input")
	}
	if got != want {
		t.Fatalf("G4 mismatch\n got: %q\nwant: %q", got, want)
	}
	got2, _ := Layout(got, "")
	if got2 != got {
		t.Fatal("G4 not idempotent")
	}
}

// TestG5JoinOn pins G5: a short fitting ON stays on the JOIN line; a JOIN
// segment with top-level AND in ON always breaks, ON at level+1 with
// AND/OR at line-start.
func TestG5JoinOn(t *testing.T) {
	in := "SELECT a FROM t1 JOIN t2 ON t1.id = t2.id"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected short join")
	}
	if got != in {
		t.Fatalf("short ON must stay inline\n got: %q\nwant: %q", got, in)
	}

	in2 := "SELECT x FROM t1 JOIN t2 ON t1.a = t2.a AND t1.b = t2.b"
	want2 := "SELECT x FROM t1\n" +
		"JOIN t2\n" +
		"\tON t1.a = t2.a\n" +
		"\tAND t1.b = t2.b"
	got2, ok := Layout(in2, "")
	if !ok {
		t.Fatal("Layout rejected AND join")
	}
	if got2 != want2 {
		t.Fatalf("AND inside ON must break\n--- got ---\n%s\n--- want ---\n%s", got2, want2)
	}

	// Long fitting ON (segment exactly 80 runes): the whole segment keeps
	// ON glued, on its own line.
	long := strings.Repeat("c", 30)
	in3 := "SELECT x FROM t1 JOIN t2 ON t1." + long + " = t2." + long
	got3, ok := Layout(in3, "")
	if !ok {
		t.Fatal("Layout rejected long join")
	}
	want3 := "SELECT x FROM t1\n" +
		"JOIN t2 ON t1." + long + " = t2." + long
	if got3 != want3 {
		t.Fatalf("long ON must keep ON glued on the JOIN line\n--- got ---\n%s\n--- want ---\n%s", got3, want3)
	}

	// One rune more and the segment breaks: ON at level+1.
	long31 := strings.Repeat("c", 31)
	in4 := "SELECT x FROM t1 JOIN t2 ON t1." + long31 + " = t2." + long31
	got4, ok := Layout(in4, "")
	if !ok {
		t.Fatal("Layout rejected overlong join")
	}
	want4 := "SELECT x FROM t1\n" +
		"JOIN t2\n" +
		"\tON t1." + long31 + " = t2." + long31
	if got4 != want4 {
		t.Fatalf("overlong ON must break ON to level+1\n--- got ---\n%s\n--- want ---\n%s", got4, want4)
	}

	for _, g := range []string{got, got2, got3, got4} {
		again, _ := Layout(g, "")
		if again != g {
			t.Fatal("G5 not idempotent")
		}
	}
}

// TestG5bJoinCap pins the documented cap: with 4+ join segments every
// JOIN gets its own line even when everything would fit (len(joins) <= 3
// enables the glue path).
func TestG5bJoinCap(t *testing.T) {
	in := "SELECT a FROM t1" +
		" JOIN t2 ON t2.x = t1.x" +
		" JOIN t3 ON t3.x = t1.x" +
		" JOIN t4 ON t4.x = t1.x" +
		" JOIN t5 ON t5.x = t1.x"
	want := "SELECT a FROM t1\n" +
		"JOIN t2 ON t2.x = t1.x\n" +
		"JOIN t3 ON t3.x = t1.x\n" +
		"JOIN t4 ON t4.x = t1.x\n" +
		"JOIN t5 ON t5.x = t1.x"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected 5-join query")
	}
	if got != want {
		t.Fatalf("4+ joins must each get their own line\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	again, _ := Layout(got, "")
	if again != got {
		t.Fatal("G5b not idempotent")
	}
}

// TestSkipReasons pins SkipString for every reason and exercises
// LayoutWithReason end-to-end, including the ReasonUnsafe token guard.
func TestSkipReasons(t *testing.T) {
	// SkipString covers every reason with a non-empty message.
	for _, r := range []SkipReason{ReasonEmpty, ReasonDDL, ReasonSQLXEscape, ReasonParse, ReasonUnsafe, ReasonNestedComment} {
		if SkipString(r) == "" {
			t.Fatalf("SkipString(%d) is empty", r)
		}
	}

	cases := []struct {
		name string
		sql  string
		want SkipReason
	}{
		{"empty", "   \n  ", ReasonEmpty},
		{"ddl", "CREATE TABLE t (id int)", ReasonDDL},
		{"sqlx escape", "SELECT id FROM t WHERE u = :u::::text", ReasonSQLXEscape},
		{"invalid sql", "SELECT FROM WHERE", ReasonParse},
		// PostgreSQL block comments nest; GoSQLX stops at the first */
		// and treats the tail as real tokens, so such literals are skipped.
		{"nested block comment", "SELECT 1 /* outer /* inner */ + 2 */", ReasonNestedComment},
		{"nested tight", "SELECT 1 /*/* x */ */", ReasonNestedComment},
		// '$' inside an identifier is not a dollar quote: the scan must
		// not give up at an identifier like a$b$.
		{"nested after dollar ident", "SELECT a$b$ FROM t /* x /* y */ */", ReasonNestedComment},
	}
	for _, tc := range cases {
		var got SkipReason
		if _, ok := LayoutWithReason(tc.sql, "", &got); ok {
			t.Fatalf("%s: Layout unexpectedly succeeded", tc.name)
		}
		if got != tc.want {
			t.Fatalf("%s: reason = %d, want %d", tc.name, got, tc.want)
		}
	}

	// ReasonUnsafe (token-stream guard) cannot be reproduced by an input
	// string: it requires the layout engine itself to want to change
	// tokens, which the whitespace-only design prevents. It is covered
	// indirectly by the atom-guard runtime path; here we only pin that
	// the guard code compiles into the same fail() funnel as other
	// reasons (ReasonParse above uses the same mechanism).

	// Control cases: "/*" inside string literals and normal flat block
	// comments must not trip the nested-comment gate.
	for _, tc := range []struct{ name, sql string }{
		{"block comment in string", "SELECT '/* not a /* comment */' FROM t"},
		{"flat block comment", "SELECT /* hello */ a FROM t"},
		{"comment before string", "SELECT /* a */ 'b' FROM t"},
		{"dollar-quoted string", "SELECT $$ /* /* $$ FROM t"},
		{"tagged dollar-quoted string", "SELECT $x$ /* /* $x$ FROM t"},
		{"comment chars in quoted identifier", `SELECT "a/*b" FROM t`},
		{"comment opener in line comment", "SELECT a -- /* /*\nFROM t"},
	} {
		var got SkipReason
		if out, ok := LayoutWithReason(tc.sql, "", &got); !ok {
			t.Fatalf("%s: Layout rejected literal (reason %d)", tc.name, got)
		} else if _, ok := LayoutWithReason(out, "", &got); !ok {
			t.Fatalf("%s: output not idempotent (reason %d)", tc.name, got)
		}
	}

	// Plain Layout with no reason pointer: unchanged contract.
	if _, ok := Layout("CREATE TABLE t (id int)", ""); ok {
		t.Fatal("plain Layout must reject DDL")
	}
}

// TestSpacedOperatorRejected pins the "< =" contract: the layout must
// never silently join spaced operator characters into a new operator
// ("=<", "<="), which would change the query's meaning behind every
// gate. GoSQLX's parse gate rejects such input outright — the pinned
// contract is ReasonParse with no output. (Direct output-based pinning
// is impossible: the gate never lets the literal reach layout.)
func TestSpacedOperatorRejected(t *testing.T) {
	for _, in := range []string{
		"SELECT a < = b FROM t",
		"SELECT a > = b FROM t",
		"SELECT a = > b FROM t",
	} {
		var r SkipReason
		out, ok := LayoutWithReason(in, "", &r)
		if ok || out != "" {
			t.Fatalf("%q: spaced operator must never be re-laid (got ok=%v out=%q)", in, ok, out)
		}
		if r != ReasonParse {
			t.Fatalf("%q: reason = %d (%s), want ReasonParse", in, r, SkipString(r))
		}
	}
}

// TestTrailingComments pins the trailing-comment behavior: a comment with
// code before it on the same source line ("trailing") rides that line
// instead of forcing a break, and contiguous lines with trailing comments
// are padded to one shared comment column. Leading comments (own source
// line) keep the old own-line behavior.
func TestTrailingComments(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "trailing comment rides its list item",
			in:   "SELECT a, b, c, d, e -- last one\nFROM t",
			want: "SELECT\n" +
				"\ta,\n" +
				"\tb,\n" +
				"\tc,\n" +
				"\td,\n" +
				"\te -- last one\n" +
				"FROM t",
		},
		{
			name: "trailing block comment keeps clause inline",
			in:   "SELECT a, b /* note */ FROM t",
			want: "SELECT a, b /* note */ FROM t",
		},
		{
			name: "trailing comment after comma rides previous item's line",
			in:   "SELECT aaa, bbb, -- note\nccc, ddd FROM t",
			want: "SELECT\n" +
				"\taaa,\n" +
				"\tbbb, -- note\n" +
				"\tccc,\n" +
				"\tddd\n" +
				"FROM t",
		},
		{
			name: "where chain with trailing comments rides and aligns",
			in:   "SELECT 1 FROM t\nWHERE aaaa = 1 -- first\nAND bbbb = 2 -- second",
			want: "SELECT 1 FROM t\n" +
				"WHERE aaaa = 1 -- first\n" +
				"\tAND bbbb = 2  -- second",
		},
	}
	for _, tc := range cases {
		got, ok := Layout(tc.in, "")
		if !ok {
			t.Fatalf("%s: Layout rejected literal", tc.name)
		}
		if got != tc.want {
			t.Fatalf("%s:\n--- got ---\n%s\n--- want ---\n%s", tc.name, got, tc.want)
		}
		again, ok := Layout(got, "")
		if !ok {
			t.Fatalf("%s: re-layout rejected its own output", tc.name)
		}
		if again != got {
			t.Fatalf("%s: not idempotent\n--- second ---\n%s\n--- first ---\n%s", tc.name, again, got)
		}
	}

	// Multiline atoms (block comments, string literals with embedded
	// newlines) advance the line counter: alignment marks must stay on
	// the comment's real line, never inside a string literal.
	multi := []string{
		"SELECT /* x\ny */ a, -- c1\n b -- c2\nFROM t",
		"SELECT 'p\nq' AS s, -- c1\n b -- c2\nFROM t",
		"SELECT f(a -- c\n) FROM t",
		"SELECT " + strings.Repeat("long_column_name_here, ", 4) + "last_item -- a very long trailing comment that pushes far past the limit\nFROM t",
		// Item ending in a comment (comma deferred) followed by an item
		// starting with a trailing comment: the ride branch must not eat
		// the deferred comma — previously this literal was silently
		// rejected (ReasonParse) instead of formatted.
		"SELECT b /* x */, /* y */ c, d, e, f FROM t",
	}
	for _, in := range multi {
		got, ok := Layout(in, "")
		if !ok {
			t.Fatalf("multiline atom case rejected: %q", in)
		}
		again, ok := Layout(got, "")
		if !ok || again != got {
			t.Fatalf("multiline atom case not idempotent\n--- in ---\n%s\n--- once ---\n%s\n--- twice ---\n%s", in, got, again)
		}
		for _, line := range strings.Split(got, "\n") {
			if strings.Contains(line, "'p") && strings.Contains(line, "  ") {
				t.Fatalf("padding leaked into a string literal line:\n%s", got)
			}
		}
	}

	// A run of adjacent >80-column lines with trailing comments must NOT
	// be padded (the shared column would exceed widthLimit): each comment
	// keeps its single separator space. Guards the target <= widthLimit
	// branch of alignTrailingComments.
	long := "SELECT 1 FROM t\n" +
		"WHERE " + strings.Repeat("x", 70) + " >= 100000 -- first\n" +
		"AND " + strings.Repeat("y", 70) + " <= 200000 -- second"
	got, ok := Layout(long, "")
	if !ok {
		t.Fatal("over-limit run case rejected")
	}
	again, ok := Layout(got, "")
	if !ok || again != got {
		t.Fatalf("over-limit run case not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got, again)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasSuffix(line, "  -- first") || strings.HasSuffix(line, "  -- second") {
			t.Fatalf("comments aligned past the 80-column limit:\n%s", got)
		}
	}
}

// TestAdjacentStringLiterals pins a PostgreSQL whitespace constraint the
// parse gate cannot see: adjacent string literals concatenate only when
// separated by pure whitespace containing a newline, so the layout must
// keep them off one line. With a comment between the literals the input
// is invalid PostgreSQL either way; the layout still keeps the literals
// on different lines (the closer-to-valid form).
func TestAdjacentStringLiterals(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		lit1     string
		lit2     string
		sameLine bool // false: must land on different lines
	}{
		{"plain", "SELECT 'a'\n'b' FROM t", "'a'", "'b'", false},
		{"block comment between", "SELECT 'x' /* c */\n'y' FROM t", "'x'", "'y'", false},
		{"line comment between", "SELECT 'x' -- c\n'y' FROM t", "'x'", "'y'", false},
		{"inline pair stays inline", "SELECT 'a', 'b' FROM t", "'a'", "'b'", true},
	}
	for _, tc := range cases {
		out, ok := Layout(tc.in, "")
		if !ok {
			t.Fatalf("%s: Layout rejected input", tc.name)
		}
		for _, line := range strings.Split(out, "\n") {
			both := strings.Contains(line, tc.lit1) && strings.Contains(line, tc.lit2)
			if both && !tc.sameLine {
				t.Fatalf("%s: literals must be on different lines (PostgreSQL concatenation), got:\n%s", tc.name, out)
			}
		}
		if tc.sameLine {
			found := false
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, tc.lit1) && strings.Contains(line, tc.lit2) {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: inline pair was broken apart:\n%s", tc.name, out)
			}
		}
		again, ok := Layout(out, "")
		if !ok || again != out {
			t.Fatalf("%s: not idempotent\n--- once ---\n%s\n--- twice ---\n%s", tc.name, out, again)
		}
	}
}

// TestGluedArgsFrameIdempotent pins the fuzz-found regression: a multiline
// args frame with content glued to the "(" line must be a fixed point —
// the padding normalization used to differ between passes.
func TestGluedArgsFrameIdempotent(t *testing.T) {
	in := "SEt( 0\n  ())"
	out, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected glued args frame")
	}
	again, ok := Layout(out, "")
	if !ok || again != out {
		t.Fatalf("glued args frame not idempotent\n--- once ---\n%q\n--- twice ---\n%q", out, again)
	}
}

// TestG5cOuterJoinMerged pins the merged "left outer join" handling: the
// tokenizer emits LEFT + "OUTER JOIN" (merged) as separate atoms; they
// form ONE join unit (no split), get UPPER CASE, and the 4-join cap
// applies.
func TestG5cOuterJoinMerged(t *testing.T) {
	in := "SELECT a FROM t1 left outer join t2 ON t2.x = t1.x AND t2.y = 1 left outer join t3 ON t3.x = t1.x left outer join t4 ON t4.x = t1.x left outer join t5 ON t5.x = t1.x"
	want := "SELECT a FROM t1\n" +
		"LEFT OUTER JOIN t2\n" +
		"\tON t2.x = t1.x\n" +
		"\tAND t2.y = 1\n" +
		"LEFT OUTER JOIN t3 ON t3.x = t1.x\n" +
		"LEFT OUTER JOIN t4 ON t4.x = t1.x\n" +
		"LEFT OUTER JOIN t5 ON t5.x = t1.x"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected left outer join query")
	}
	if got != want {
		t.Fatalf("LEFT OUTER JOIN must be one glued unit\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	again, _ := Layout(got, "")
	if again != got {
		t.Fatal("G5c not idempotent")
	}
}

// TestG6CTE pins G6: "WITH name AS (" with the subquery content at
// level+1, ")" at the WITH level, the source comma glued after ")", and
// a blank line between CTE definitions.
func TestG6CTE(t *testing.T) {
	longA := strings.Repeat("a", 40)
	longB := strings.Repeat("b", 40)
	in := "WITH t_a AS (SELECT " + longA + ", " + longB + " FROM t1), " +
		"t_b AS (SELECT t2.some_column_name_here, t2.another_long_column FROM t2 WHERE x = 1 AND y = 2)"
	want := "WITH t_a AS (\n" +
		"\tSELECT\n" +
		"\t\t" + longA + ",\n" +
		"\t\t" + longB + "\n" +
		"\tFROM t1\n" +
		"),\n" +
		"\n" +
		"t_b AS (\n" +
		"\tSELECT t2.some_column_name_here, t2.another_long_column FROM t2\n" +
		"\tWHERE x = 1\n" +
		"\t\tAND y = 2\n" +
		")\n" +
		"SELECT * FROM t_a JOIN t_b ON t_a.x = t_b.y"
	// The main statement is appended to the input.
	in += " SELECT * FROM t_a JOIN t_b ON t_a.x = t_b.y"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected G6 input")
	}
	if got != want {
		t.Fatalf("G6 mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	got2, _ := Layout(got, "")
	if got2 != got {
		t.Fatal("G6 not idempotent")
	}
}

// TestG7Case pins G7: fitting WHEN lines stay one line; a too-wide WHEN
// breaks at its AND (line start at CASE level+1); END at the CASE level.
func TestG7Case(t *testing.T) {
	c1 := strings.Repeat("x", 30)
	c2 := strings.Repeat("y", 30)
	in := "SELECT CASE WHEN a = 1 THEN 'one' WHEN " + c1 + " = 1 AND " + c2 + " = 2 THEN 'two' ELSE 'z' END FROM t"
	want := "SELECT\n" +
		"\tCASE\n" +
		"\t\tWHEN a = 1 THEN 'one'\n" +
		"\t\tWHEN " + c1 + " = 1\n" +
		"\t\tAND " + c2 + " = 2\n" +
		"\t\tTHEN 'two'\n" +
		"\t\tELSE 'z'\n" +
		"\tEND\n" +
		"FROM t"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected G7 input")
	}
	if got != want {
		t.Fatalf("G7 mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	got2, _ := Layout(got, "")
	if got2 != got {
		t.Fatal("G7 not idempotent")
	}
}

// TestG8InsertOnConflict pins G8: args-preserved multiline column list,
// VALUES tuples one per line past 4 items, ON CONFLICT ... DO UPDATE with
// the SET assignments broken (12 > 4) and WHERE after.
func TestG8InsertOnConflict(t *testing.T) {
	in := "INSERT INTO app.t (\n\ta, b, c\n)\n" +
		"VALUES ($1, $2, $3), ($4, $5, $6), ($7, $8, $9), ($10, $11, $12), ($13, $14, $15)\n" +
		"ON CONFLICT (a) DO UPDATE SET data_class = $16, expires_at = $17, bucket_id = $18, key = $19, size = $20, etag = $21, owner = $22, flags = $23, version = $24, state = $25, note = $26, extra = $27\n" +
		"WHERE app.t.bucket_id = $28"
	want := "INSERT INTO app.t (\n" +
		"\ta, b, c\n" +
		")\n" +
		"VALUES\n" +
		"\t($1, $2, $3),\n" +
		"\t($4, $5, $6),\n" +
		"\t($7, $8, $9),\n" +
		"\t($10, $11, $12),\n" +
		"\t($13, $14, $15)\n" +
		"ON CONFLICT (a) DO UPDATE\n" +
		"SET\n" +
		"\tdata_class = $16,\n" +
		"\texpires_at = $17,\n" +
		"\tbucket_id = $18,\n" +
		"\tkey = $19,\n" +
		"\tsize = $20,\n" +
		"\tetag = $21,\n" +
		"\towner = $22,\n" +
		"\tflags = $23,\n" +
		"\tversion = $24,\n" +
		"\tstate = $25,\n" +
		"\tnote = $26,\n" +
		"\textra = $27\n" +
		"WHERE app.t.bucket_id = $28"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected G8 input")
	}
	if got != want {
		t.Fatalf("G8 mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	got2, _ := Layout(got, "")
	if got2 != got {
		t.Fatal("G8 not idempotent")
	}
}

// TestG10Unit pins G10: the indent unit is a parameter; both "\t" and
// "  " work and only the indentation changes.
func TestG10Unit(t *testing.T) {
	in := "SELECT a FROM t WHERE x = $1 AND y = 2"
	gotTab, ok := Layout(in, "\t")
	if !ok {
		t.Fatal("Layout rejected tab unit")
	}
	wantTab := "SELECT a FROM t\nWHERE x = $1\n\tAND y = 2"
	if gotTab != wantTab {
		t.Fatalf("tab unit mismatch\n got: %q\nwant: %q", gotTab, wantTab)
	}
	got2sp, ok := Layout(in, "  ")
	if !ok {
		t.Fatal("Layout rejected two-space unit")
	}
	want2sp := "SELECT a FROM t\nWHERE x = $1\n  AND y = 2"
	if got2sp != want2sp {
		t.Fatalf("two-space unit mismatch\n got: %q\nwant: %q", got2sp, want2sp)
	}
	for u, g := range map[string]string{"\t": gotTab, "  ": got2sp} {
		again, ok := Layout(g, u)
		if !ok || again != g {
			t.Fatalf("G10 not idempotent for unit %q:\n%s\n---\n%s", u, g, again)
		}
	}
}

// TestG11GatesAndSurvivors pins the remaining G11 gates and survivors:
// DDL skip, :::: skip, verbs, $1, Cyrillic comments, BETWEEN AND inline,
// args-preserve re-indentation, atom guard, invalid SQL, ignore comment.
func TestG11GatesAndSurvivors(t *testing.T) {
	// DDL and sqlx escapes are refused.
	for _, in := range []string{
		"CREATE INDEX x ON t(c) WHERE cond;",
		"CREATE TABLE mailbox (id INTEGER PRIMARY KEY)",
		"ALTER TABLE t ADD COLUMN c INT",
		"SELECT a WHERE b = ::::param",
	} {
		if got, ok := Layout(in, ""); ok {
			t.Fatalf("Layout must skip %q, got %q", in, got)
		}
	}
	// Invalid SQL is refused.
	for _, in := range []string{"SELECT FROM WHERE", "SELECT ??? not sql"} {
		if got, ok := Layout(in, ""); ok {
			t.Fatalf("Layout accepted invalid SQL %q -> %q", in, got)
		}
	}
	// Verbs and placeholders survive.
	in := "SELECT a, %s, %10s, %%, %d FROM t WHERE b = $1 AND c = 2"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected verb input")
	}
	for _, v := range []string{"%s", "%10s", "%%", "%d", "$1"} {
		if !strings.Contains(got, v) {
			t.Fatalf("output missing %q:\n%s", v, got)
		}
	}
	// The full fmt verb alphabet is masked: %g/%-8.2f/%e/%o do not parse
	// as SQL, so without masking these literals would be skipped.
	for _, verb := range []string{"%g", "%-8.2f", "%e", "%o", "%c", "%U"} {
		in := "SELECT a FROM t WHERE x = " + verb + " AND y = 1"
		got, ok := Layout(in, "")
		if !ok {
			t.Fatalf("Layout rejected literal with verb %s", verb)
		}
		if !strings.Contains(got, verb) {
			t.Fatalf("output missing verb %s:\n%s", verb, got)
		}
	}
	// Cyrillic comment on its own line, leading comma normalized.
	in2 := "SELECT a\n\t-- русский комментарий\n, b FROM t"
	want2 := "SELECT\n" +
		"\ta\n" +
		"\t-- русский комментарий\n" +
		"\t, b\n" +
		"FROM t"
	got2, ok := Layout(in2, "")
	if !ok {
		t.Fatal("Layout rejected Cyrillic comment input")
	}
	if got2 != want2 {
		t.Fatalf("Cyrillic comment mismatch\n--- got ---\n%s\n--- want ---\n%s", got2, want2)
	}
	// BETWEEN-AND stays inline inside the broken chain.
	in3 := "SELECT a FROM t WHERE x BETWEEN 1 AND 10 AND y = 2"
	want3 := "SELECT a FROM t\n" +
		"WHERE x BETWEEN 1 AND 10\n" +
		"\tAND y = 2"
	got3, ok := Layout(in3, "")
	if !ok {
		t.Fatal("Layout rejected BETWEEN input")
	}
	if got3 != want3 {
		t.Fatalf("BETWEEN mismatch\n--- got ---\n%s\n--- want ---\n%s", got3, want3)
	}
	// Args-preserve json_build_object: padding kept, re-indented.
	in4 := "SELECT json_build_object(\n'cdn',           W.cdn,\n'features',      W.features\n) FROM t"
	want4 := "SELECT\n" +
		"\tjson_build_object(\n" +
		"\t\t'cdn',           W.cdn,\n" +
		"\t\t'features',      W.features\n" +
		"\t) FROM t"
	got4, ok := Layout(in4, "")
	if !ok {
		t.Fatal("Layout rejected args frame input")
	}
	if got4 != want4 {
		t.Fatalf("args frame padding mismatch\n--- got ---\n%s\n--- want ---\n%s", got4, want4)
	}
	for _, g := range []string{got, got2, got3, got4} {
		again, _ := Layout(g, "")
		if again != g {
			t.Fatalf("G11 case not idempotent:\n%s\n---\n%s", g, again)
		}
	}
}

// TestG12Convergence pins G12: v3 river fixtures, old leading-comma
// fixtures and whitespace-collapsed variants all converge to the same v4
// fixed point (for inputs without comments or args frames).
func TestG12Convergence(t *testing.T) {
	inputs := []string{
		complexSample,
		// original hand-written shape (merged line, double space)
		strings.Replace(complexSample,
			"\t\tSELECT COALESCE(array_to_json(array_agg(S2.*)), '[]')\n\t\tFROM (",
			"\t\tSELECT COALESCE(array_to_json(array_agg(S2.*)), '[]')  FROM (", 1),
		"SELECT a, b FROM t WHERE x = $1 AND y = 2 ORDER BY a",
		"SELECT a, b FROM t\nWHERE x = 1",
		"WITH a AS (SELECT x FROM t1), b AS (SELECT y FROM t2) SELECT * FROM a JOIN b ON a.x = b.y",
		"UPDATE albums\n   SET release_date = '1990-01-01 01:01:01.00000'\n WHERE title = 'The New Danger';",
	}
	for _, in := range inputs {
		once, ok := Layout(in, "")
		if !ok {
			t.Fatalf("Layout rejected %q", in)
		}
		twice, ok := Layout(once, "")
		if !ok {
			t.Fatalf("Layout rejected its own output")
		}
		if once != twice {
			t.Fatalf("not idempotent for input:\n%s\n--- once ---\n%s\n--- twice ---\n%s", in, once, twice)
		}
	}
	// Whitespace-collapsed input formats identically (no comments or args
	// frames in these samples).
	collapse := func(s string) string {
		return strings.Join(strings.Fields(s), " ")
	}
	for _, in := range []string{
		"SELECT a, b FROM t\nWHERE x = $1 AND y = 2\nORDER BY a",
		"WITH a AS (SELECT x FROM t1), b AS (SELECT y FROM t2) SELECT * FROM a JOIN b ON a.x = b.y",
		"SELECT c1, c2, c3, c4, c5, c6\nFROM app.t\nWHERE k = 1",
	} {
		a, ok := Layout(in, "")
		if !ok {
			t.Fatalf("Layout rejected %q", in)
		}
		b, ok := Layout(collapse(in), "")
		if !ok {
			t.Fatalf("Layout rejected collapsed %q", collapse(in))
		}
		if a != b {
			t.Fatalf("collapsed input diverged\n--- original ---\n%s\n--- collapsed ---\n%s", a, b)
		}
	}
}

// TestLayoutKeywordFalsePositives pins that clause-keyword spellings used
// as expressions stay inline: function calls named replace/set_config,
// "WITH TIME ZONE"/"WITH ORDINALITY" modifiers, AND inside CASE, and
// FOR NO KEY UPDATE / FOR SHARE row locks.
func TestLayoutKeywordFalsePositives(t *testing.T) {
	tests := []struct {
		name, in, wantSub string
	}{
		{"replace call", "SELECT replace(a, 'x', 'y'), b FROM t", "SELECT replace(a, 'x', 'y'), b FROM t"},
		{"set_config call", "SELECT set_config('a', 'b', true) FROM t", "SELECT set_config('a', 'b', TRUE) FROM t"},
		{"with time zone", "SELECT x::timestamp with time zone FROM t", "x::timestamp WITH time zone"},
		{"with ordinality", "SELECT * FROM unnest(a) with ordinality", "WITH ordinality"},
		{"and in case", "SELECT CASE WHEN a = 1 AND b = 2 THEN 1 END FROM t", "WHEN a = 1 AND b = 2 THEN 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Layout(tc.in, "")
			if !ok {
				t.Fatalf("Layout rejected %q", tc.in)
			}
			if !strings.Contains(got, tc.wantSub) {
				t.Fatalf("expected %q in output:\n%s", tc.wantSub, got)
			}
			got2, ok := Layout(got, "")
			if !ok || got2 != got {
				t.Fatalf("not idempotent:\n%s\n---\n%s", got, got2)
			}
		})
	}
	// Row-lock variants must stay inline (FOR UPDATE still breaks).
	for _, in := range []string{"SELECT 1 FROM t FOR NO KEY UPDATE", "SELECT 1 FROM t FOR SHARE"} {
		got, ok := Layout(in, "")
		if !ok {
			t.Fatalf("Layout rejected %q", in)
		}
		if strings.Count(got, "\n") != 0 {
			t.Fatalf("row lock must stay on one line:\n%s", got)
		}
	}
	// FOR UPDATE is an always-own-line unit (D9).
	got, ok := Layout("SELECT 1 FROM t FOR UPDATE SKIP LOCKED", "")
	if !ok {
		t.Fatal("Layout rejected FOR UPDATE input")
	}
	want := "SELECT 1 FROM t\nFOR UPDATE SKIP LOCKED"
	if got != want {
		t.Fatalf("FOR UPDATE must sit on its own line\n got: %q\nwant: %q", got, want)
	}
}

// TestLayoutIdentifierParenSpacing pins that the source spacing between a
// bare identifier and "(" is preserved (table name vs function call).
func TestLayoutIdentifierParenSpacing(t *testing.T) {
	glued := map[string]string{
		"bool_and(":          "SELECT bool_and(A.archived_at IS NOT NULL) FROM t",
		"tool_file_changes(": "SELECT * FROM tool_file_changes(x)",
	}
	spaced := map[string]string{
		"tool_file_changes (": "INSERT INTO tool_file_changes (session_id) VALUES (1)",
	}
	for want, in := range glued {
		out, ok := Layout(in, "")
		if !ok {
			t.Fatalf("Layout rejected %q", in)
		}
		if !strings.Contains(out, want) {
			t.Fatalf("%q must stay glued in output:\n%s", want, out)
		}
	}
	for want, in := range spaced {
		out, ok := Layout(in, "")
		if !ok {
			t.Fatalf("Layout rejected %q", in)
		}
		if !strings.Contains(out, want) {
			t.Fatalf("source space in %q must be kept in output:\n%s", want, out)
		}
	}
}

// TestLayoutCommentsOwnLine pins that a comment breaks its group and sits
// on its own line at the item level.
func TestLayoutCommentsOwnLine(t *testing.T) {
	in := "SELECT a -- note\nFROM t"
	// A trailing comment (code before it on the same source line) rides
	// its line; a leading comment keeps its own line (TestTrailingComments).
	want := "SELECT a -- note\nFROM t"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected comment input")
	}
	if got != want {
		t.Fatalf("comment mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestLayoutSubqueryInsideArgsFrameStable pins that a fitting args frame
// with an inline subquery stays on one line.
func TestLayoutSubqueryInsideArgsFrameStable(t *testing.T) {
	in := "SELECT COALESCE((SELECT a, b FROM t WHERE x = 1), x) AS v FROM u"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected args-subquery input")
	}
	got2, ok := Layout(got, "")
	if !ok || got2 != got {
		t.Fatalf("not stable for args-subquery input\n once: %q\n twice: %q", got, got2)
	}
	if !strings.Contains(got, "COALESCE((SELECT a, b FROM t WHERE x = 1), x)") {
		t.Fatalf("inline args frame changed:\n%s", got)
	}
}

// TestLayoutAtomGuard verifies that the layout engine never changes token
// text and never adds or drops tokens: the raw token slices of input and
// output must be identical.
func TestLayoutAtomGuard(t *testing.T) {
	inputs := []string{
		complexSample,
		"SELECT a, %s, %10s, %%, %d FROM t WHERE b = $1 AND c = 2",
		"SELECT E.meta #>> '{live,type}', 'a''b'::UUID FROM t WHERE x BETWEEN 1 AND 10",
		"WITH a AS (SELECT x FROM t1), b AS (SELECT y FROM t2) SELECT * FROM a JOIN b ON a.x = b.y",
		"INSERT INTO app.cleanup_queue (entry_id, data_class, payload, not_before, enqueued_at)\nVALUES ($1, $2, $3, $4, $4)\nON CONFLICT (entry_id) DO NOTHING",
	}
	for _, in := range inputs {
		out, ok := Layout(in, "")
		if !ok {
			t.Fatalf("Layout rejected %q", in)
		}
		inSlices := rawAtoms(in)
		outSlices := rawAtoms(out)
		if !sameTokenStreams(inSlices, outSlices) {
			t.Fatalf("token stream changed for %q", in)
		}
	}
}

// TestKeywordUppercase pins keyword casing: keyword-classified tokens are
// emitted UPPER CASE, lowercase input is idempotent, and keywords inside
// args-preserved raw frames are uppercased too.
func TestKeywordUppercase(t *testing.T) {
	const want = "SELECT id FROM t WHERE x = 1"
	got, ok := Layout("select id from t where x = 1", "")
	if !ok {
		t.Fatal("Layout rejected lowercase keyword input")
	}
	if got != want {
		t.Fatalf("keyword casing mismatch\n got: %q\nwant: %q", got, want)
	}
	got2, ok := Layout(got, "")
	if !ok || got2 != got {
		t.Fatalf("uppercase output not a fixed point\n--- once ---\n%s\n--- twice ---\n%s", got, got2)
	}
	// Mixed-case input converges to the same fixed point.
	mixed, ok := Layout("SeLeCt id FrOm t WhErE x = 1", "")
	if !ok || mixed != want {
		t.Fatalf("mixed-case input mismatch\n got: %q\nwant: %q", mixed, want)
	}
	// Keywords inside a multiline args-preserved frame are uppercased.
	in := "SELECT fn(\n  case when x then 1 end\n) FROM t"
	got3, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected args-frame input")
	}
	if want := "SELECT\n\tfn(\n\t\tCASE WHEN x THEN 1 END\n\t) FROM t"; got3 != want {
		t.Fatalf("args-frame keyword casing mismatch\n got: %q\nwant: %q", got3, want)
	}
	got4, ok := Layout(got3, "")
	if !ok || got4 != got3 {
		t.Fatalf("args-frame output not a fixed point\n--- once ---\n%s\n--- twice ---\n%s", got3, got4)
	}
}

// TestKeywordCasingSparesNonKeywords pins that plain identifiers, quoted
// identifiers and string literals keep the author's casing.
func TestKeywordCasingSparesNonKeywords(t *testing.T) {
	in := `select first_name as fn from "user" where first_name = 'abc'`
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected mixed input")
	}
	want := `SELECT first_name AS fn FROM "user" WHERE first_name = 'abc'`
	if got != want {
		t.Fatalf("non-keyword casing changed\n got: %q\nwant: %q", got, want)
	}
	got2, ok := Layout(got, "")
	if !ok || got2 != got {
		t.Fatalf("not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got, got2)
	}
}

// TestKeywordLikeIdentifier pins the whitelist rule: a column whose name
// the tokenizer classifies as a keyword ("key") keeps the author's casing
// (only whitelisted clause keywords are uppercased), and the output still
// parses (Layout's parse gate).
func TestKeywordLikeIdentifier(t *testing.T) {
	got, ok := Layout("select key, owner from t", "")
	if !ok {
		t.Fatal("Layout rejected keyword-like identifier input")
	}
	if want := "SELECT key, owner FROM t"; got != want {
		t.Fatalf("keyword-like identifier mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestAfterDotKeywordKeepsCasing pins the qualified-name rule: sec.key is
// part of a qualified name and keeps the author's casing; the helper
// itself refuses to uppercase a whitelisted spelling after a dot.
func TestAfterDotKeywordKeepsCasing(t *testing.T) {
	got, ok := Layout("select sec.key from sec join t on t.id = sec.id", "")
	if !ok {
		t.Fatal("Layout rejected after-dot input")
	}
	if want := "SELECT sec.key FROM sec JOIN t ON t.id = sec.id"; got != want {
		t.Fatalf("after-dot casing mismatch\n got: %q\nwant: %q", got, want)
	}
	got2, ok := Layout(got, "")
	if !ok || got2 != got {
		t.Fatalf("after-dot output not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got, got2)
	}
	// White-box: a whitelisted spelling after a period keeps author casing.
	if v := upperKeyword("left", true); v != "left" {
		t.Fatalf("after-dot whitelisted keyword must keep casing, got %q", v)
	}
	if v := upperKeyword("left", false); v != "LEFT" {
		t.Fatalf("whitelisted keyword must be uppercased, got %q", v)
	}
}

// TestIntervalLiteralUppercased pins that INTERVAL stays on the curated
// whitelist: interval '5 minutes' is normalized to INTERVAL.
func TestIntervalLiteralUppercased(t *testing.T) {
	got, ok := Layout("select interval '5 minutes' from t", "")
	if !ok {
		t.Fatal("Layout rejected interval input")
	}
	if want := "SELECT INTERVAL '5 minutes' FROM t"; got != want {
		t.Fatalf("interval casing mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBindNameLiteralSkipped pins that sqlx-style ":name" bind parameters
// do not parse, so such literals are skipped and never rewritten (a
// keyword-classified name like :key could otherwise become :KEY). If
// GoSQLX ever starts parsing ":name", add a bind-name exclusion to the
// keyword uppercasing.
func TestBindNameLiteralSkipped(t *testing.T) {
	for _, in := range []string{
		"select id from t where k = :key and y = :type",
		"select id from t where k = :name",
	} {
		if got, ok := Layout(in, ""); ok {
			t.Fatalf("bind-name literal must be skipped, got %q", got)
		}
	}
}

// TestKnownLimits documents narrow, deliberate exceptions to the rules:
//   - a broken list keeps only the commas the source had; no comma is
//     ever added after the last item (whitespace/no-new-tokens invariant);
//   - an overflowing single-line args frame fills (one item per line
//     remains only for a frame containing a comment: its inline trailMark
//     alignment is not reproducible across passes);
//   - the fit check measures the level indent and the current line, not
//     the processor-applied base indent.
func TestKnownLimits(t *testing.T) {
	// No comma after the last item of a broken list.
	in := "SELECT alpha, beta, gamma, delta, epsilon, zeta FROM t"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout rejected input")
	}
	if strings.Contains(got, "zeta,") {
		t.Fatalf("a comma must never be added after the last item:\n%s", got)
	}
	// Single-line overflowing args frame with two long args fills: each
	// arg lands on its own line at level+1, ")" returns to the frame
	// level, and the result is a fixed point (pass 2 re-routes the frame
	// through emitArgsLines and must reproduce the same bytes).
	long := strings.Repeat("arg", 40)
	in2 := "SELECT fn(" + long + ", " + long + ") FROM t"
	got2, ok := Layout(in2, "")
	if !ok {
		t.Fatal("Layout rejected long args input")
	}
	if strings.Contains(got2, long+", "+long) {
		t.Fatalf("overflowing args frame must fill, not stay inline:\n%s", got2)
	}
	again2, ok := Layout(got2, "")
	if !ok || again2 != got2 {
		t.Fatalf("args frame fill not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got2, again2)
	}
	// A single-line args frame containing a comment stays inline: pass 2
	// would re-emit it through emitArgsLines, whose raw lines carry no
	// trailMarks, and comment alignment would drift between passes.
	in3 := "SELECT fn(" + long + ", /* note */ " + long + ") FROM t"
	got3, ok := Layout(in3, "")
	if !ok {
		t.Fatal("Layout rejected comment args input")
	}
	if !strings.Contains(got3, "fn("+long+", /* note */ "+long+")") {
		t.Fatalf("args frame with a comment must stay inline:\n%s", got3)
	}
}

// TestArgsFrameFill pins Wadler-style fill for overflowing single-line
// args frames: items ride the current line while they fit, otherwise a
// fresh line at level+1; the first item always starts a new line (the
// shape emitArgsLines reproduces on pass 2); ")" returns to the frame's
// level with the line left extendable (") FROM t" glues on both passes).
func TestArgsFrameFill(t *testing.T) {
	// Many short args: two args per line would fit; fill packs as many
	// as fit under the 80-column limit per line.
	var b strings.Builder
	b.WriteString("SELECT fn(")
	for i := 0; i < 24; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(fmt.Sprintf("arg%02d", i))
	}
	b.WriteString(") FROM t")
	got, ok := Layout(b.String(), "")
	if !ok {
		t.Fatal("Layout rejected many-short-args input")
	}
	if !strings.Contains(got, "(\n") {
		t.Fatalf("overflowing args frame must break:\n%s", got)
	}
	again, ok := Layout(got, "")
	if !ok || again != got {
		t.Fatalf("fill not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got, again)
	}
	if !sameTokenStreams(rawAtoms(b.String()), rawAtoms(got)) {
		t.Fatal("token stream changed")
	}

	// Nested overflow: the inner frame fills inside the outer one.
	inner := strings.Repeat("x", 70)
	in2 := "SELECT wrap(aa, fn(" + inner + ", " + inner + ")) FROM t"
	got2, ok := Layout(in2, "")
	if !ok {
		t.Fatal("Layout rejected nested args input")
	}
	if strings.Contains(got2, inner+", "+inner) {
		t.Fatalf("nested overflowing frame must fill:\n%s", got2)
	}
	again2, ok := Layout(got2, "")
	if !ok || again2 != got2 {
		t.Fatalf("nested fill not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got2, again2)
	}

	// An item ending exactly at column 80 must not ride the previous
	// line (the fit check counts its trailing comma). An item whose own
	// width leaves no room for the comma may overhang by exactly that
	// comma — same class as an unbreakable-token overhang.
	wide := strings.Repeat("x", 79)
	in3 := "SELECT (" + wide + ", b, c, d) FROM t"
	got3, ok := Layout(in3, "")
	if !ok {
		t.Fatal("Layout rejected wide-first-arg input")
	}
	for i, ln := range strings.Split(got3, "\n") {
		n := len([]rune(strings.TrimSuffix(ln, ",")))
		if n > 80 {
			t.Fatalf("line %d is %d columns (> 80): %q", i+1, n, ln)
		}
	}
	again3, ok := Layout(got3, "")
	if !ok || again3 != got3 {
		t.Fatalf("wide-arg fill not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got3, again3)
	}

	// A short clause frame must be measured on its own fresh line, not
	// on the previous clause's stale full line (lineOpen=false,
	// curRunes leftover): the frame fits inline on the fresh line the
	// clause opens. Falls in the regression window found by brute force
	// (item width 74 at level+1 leaves the line at 74 columns).
	item := strings.Repeat("w", 74)
	in4 := "SELECT a, " + item + " ORDER BY (x, y)"
	got4, ok := Layout(in4, "")
	if !ok {
		t.Fatal("Layout rejected stale-line frame input")
	}
	if !strings.Contains(got4, "ORDER BY (x, y)") {
		t.Fatalf("short clause frame must stay inline:\n%s", got4)
	}
	again4, ok := Layout(got4, "")
	if !ok || again4 != got4 {
		t.Fatalf("stale-line frame not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got4, again4)
	}
}

// TestValuesFrameCommentFallback pins that a comment between VALUES and
// "(" does not reach the glue branch (which would drop it and make
// Layout reject the statement) — the statement still formats and stays
// idempotent. Multi-row VALUES keeps the generic broken-list path.
func TestValuesFrameCommentFallback(t *testing.T) {
	in := "INSERT INTO t (a, b)\nVALUES /* c */ (\n\t$1, $2\n)"
	got, ok := Layout(in, "")
	if !ok {
		t.Fatal("Layout must format a comment-before-paren VALUES frame")
	}
	if !strings.Contains(got, "/* c */") {
		t.Fatalf("comment must survive:\n%s", got)
	}
	got2, ok := Layout(got, "")
	if !ok || got2 != got {
		t.Fatalf("not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got, got2)
	}
	in2 := "INSERT INTO t (a, b)\nVALUES (\n\t$1, $2\n), (\n\t$3, $4\n)"
	got3, ok := Layout(in2, "")
	if !ok {
		t.Fatal("Layout rejected multi-row VALUES")
	}
	got4, ok := Layout(got3, "")
	if !ok || got4 != got3 {
		t.Fatalf("multi-row VALUES not idempotent\n--- once ---\n%s\n--- twice ---\n%s", got3, got4)
	}
}

// TestG14LineCommentNeverSwallowsTokens pins the line-comment guard: a
// "--" comment is always the last thing on its line; no token may be
// appended after it (it would be swallowed by the comment).
func TestG14LineCommentNeverSwallowsTokens(t *testing.T) {
	tests := []struct {
		in, wantVisible string
	}{
		{"SELECT a FROM t LIMIT 10 -- c\nOFFSET 5", "OFFSET 5"},
		{"INSERT INTO t -- c\n(a, b) VALUES (1, 2)", "VALUES (1, 2)"},
		{"SELECT a FROM t -- c\nORDER BY a", "ORDER BY a"},
	}
	for _, tc := range tests {
		out, ok := Layout(tc.in, "")
		if !ok {
			t.Fatalf("Layout rejected %q", tc.in)
		}
		visible := false
		for _, line := range strings.Split(out, "\n") {
			if !strings.Contains(line, "--") && strings.Contains(line, tc.wantVisible) {
				visible = true
			}
		}
		if !visible {
			t.Fatalf("%q must survive outside the comment in:\n%s", tc.wantVisible, out)
		}
		again, ok := Layout(out, "")
		if !ok || again != out {
			t.Fatalf("not idempotent for %q\n--- once ---\n%s\n--- twice ---\n%s", tc.in, out, again)
		}
	}
}

// TestProcessBaseIndentFixedPoint pins that repeated Process runs with a
// BaseIndent whose whitespace differs from the SQL base are a fixed point
// (no base growth across runs).
func TestProcessBaseIndentFixedPoint(t *testing.T) {
	src := "package p\n\nconst q = `\nselect id, name\nfrom t\nwhere x = 1 and y = 2`\n"
	opt := Options{Dialect: keywords.DialectPostgreSQL, BaseIndent: "  ", Unit: "\t"}
	out, changed, err := Process("test.go", []byte(src), opt)
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if !changed {
		t.Fatal("expected a change")
	}
	for i := 0; i < 3; i++ {
		out2, changed2, err := Process("test.go", out, opt)
		if err != nil {
			t.Fatalf("run %d error: %v", i, err)
		}
		if changed2 || string(out2) != string(out) {
			t.Fatalf("run %d not a fixed point:\n--- in ---\n%s\n--- out ---\n%s", i, out, out2)
		}
	}
}

// rawAtoms tokenizes sql (with format verbs masked, like Layout does) and
// returns the atom stream (token atoms only are compared by callers that
// need text; comments are included to catch dropped comments).
func rawAtoms(sql string) []atom {
	verbs := verbRe.FindAllStringIndex(sql, -1)
	masked := maskSpans(sql, verbs)
	atoms, ok := atomize(sql, masked)
	if !ok {
		return nil
	}
	return atoms
}

// sameTokenStreams reports whether two atom streams match: keyword-class
// atoms compare case-insensitively (casing is normalized), everything
// else byte-exact (mirrors the runtime guard in Layout).
func sameTokenStreams(a, b []atom) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameAtomText(&a[i], &b[i]) {
			return false
		}
	}
	return true
}
