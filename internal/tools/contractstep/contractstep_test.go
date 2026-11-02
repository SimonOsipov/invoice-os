package main

import (
	"slices"
	"strings"
	"testing"
)

const testFile = "migrations/x.sql"

func upOf(body string) string { return "-- +goose Up\n" + body + "\n" }

func scanUp(t *testing.T, body string) Result {
	t.Helper()
	r, err := Scan(testFile, upOf(body))
	if err != nil {
		t.Fatalf("Scan(%q): %v", body, err)
	}
	return r
}

func objects(r Result) []string {
	var out []string
	for _, tr := range r.Triggers {
		out = append(out, tr.Object)
	}
	return out
}

func evalUp(t *testing.T, body string) ([]string, bool) {
	t.Helper()
	return Evaluate([]Result{scanUp(t, body)})
}

func unmarkedLine(file, stmt, obj string) string {
	return file + ": " + stmt + " deletes or renames schema that a running revision may read. Write it in a later deploy, or add to the Up section: -- contract-step " + obj + ": <why no running revision reads it>"
}

func wantLines(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("lines\n got: %q\nwant: %q", got, want)
	}
}

func TestUpSection_IgnoresDownStatementsAndMarks(t *testing.T) {
	r, err := Scan(testFile, "-- +goose Up\nSELECT 1;\n-- +goose Down\nDROP TABLE t; -- contract-step t: why\n")
	if err != nil || len(r.Triggers) != 0 || len(r.Marks) != 0 {
		t.Fatalf("got %+v, %v", r, err)
	}
}

func TestUpSection_NoDownLineReadsToEndOfFile(t *testing.T) {
	if r := scanUp(t, "DROP TABLE t;"); len(r.Triggers) != 1 {
		t.Fatalf("got %+v", r)
	}
}

func TestUpSection_NoUpLineIsAnError(t *testing.T) {
	_, err := Scan(testFile, "DROP TABLE t;\n")
	if err == nil || !strings.Contains(err.Error(), testFile) || !strings.Contains(err.Error(), "-- +goose Up") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpSection_TextBeforeUpIsNotRead(t *testing.T) {
	r, err := Scan(testFile, "-- +goose NO TRANSACTION\n-- DROP TABLE t\n-- contract-step t: why\n-- +goose Up\nSELECT 1;\n")
	if err != nil || len(r.Triggers) != 0 || len(r.Marks) != 0 {
		t.Fatalf("got %+v, %v", r, err)
	}
}

func TestTriggers_EachForm(t *testing.T) {
	cases := []struct {
		sql  string
		objs []string
		stmt []string
	}{
		{"DROP TABLE t", []string{"t"}, []string{"DROP TABLE t"}},
		{"drop table if exists t cascade", []string{"t"}, []string{"DROP TABLE t"}},
		{"DROP TABLE a, b", []string{"a", "b"}, []string{"DROP TABLE a", "DROP TABLE b"}},
		{"ALTER TABLE t DROP COLUMN c", []string{"t.c"}, []string{"ALTER TABLE t DROP COLUMN c"}},
		{"ALTER TABLE t DROP c", []string{"t.c"}, []string{"ALTER TABLE t DROP COLUMN c"}},
		{"alter table if exists only t drop column if exists c cascade", []string{"t.c"}, []string{"ALTER TABLE t DROP COLUMN c"}},
		{"ALTER TABLE t RENAME COLUMN a TO b", []string{"t.a"}, []string{"ALTER TABLE t RENAME COLUMN a TO b"}},
		{"ALTER TABLE t RENAME a TO b", []string{"t.a"}, []string{"ALTER TABLE t RENAME COLUMN a TO b"}},
		{"ALTER TABLE t RENAME TO u", []string{"t"}, []string{"ALTER TABLE t RENAME TO u"}},
		{"DROP FUNCTION f(int)", []string{"f"}, []string{"DROP FUNCTION f(int)"}},
		{"DROP FUNCTION IF EXISTS public.f()", []string{"public.f"}, []string{"DROP FUNCTION public.f()"}},
		{"DROP FUNCTION f(int), g()", []string{"f", "g"}, []string{"DROP FUNCTION f(int)", "DROP FUNCTION g()"}},
		{"DROP FUNCTION f(int, text)", []string{"f"}, []string{"DROP FUNCTION f(int, text)"}},
		{"DROP TYPE ty", []string{"ty"}, []string{"DROP TYPE ty"}},
	}
	for _, c := range cases {
		r := scanUp(t, c.sql+";")
		var stmts []string
		for _, tr := range r.Triggers {
			stmts = append(stmts, tr.Statement)
		}
		if !slices.Equal(objects(r), c.objs) || !slices.Equal(stmts, c.stmt) {
			t.Errorf("%q: objects %q statements %q, want %q %q", c.sql, objects(r), stmts, c.objs, c.stmt)
		}
	}
}

func TestTriggers_MultiActionAlterTableFindsEachAction(t *testing.T) {
	r := scanUp(t, "ALTER TABLE t ADD COLUMN n numeric(10,2) DEFAULT 'a, b', DROP COLUMN x, DROP COLUMN y;")
	if got := objects(r); !slices.Equal(got, []string{"t.x", "t.y"}) {
		t.Fatalf("got %q", got)
	}
}

func TestNonTriggers_Table(t *testing.T) {
	for _, sql := range []string{
		"ALTER TABLE t DROP CONSTRAINT c",
		"ALTER TABLE t ADD COLUMN n int, DROP CONSTRAINT c",
		"DROP INDEX i",
		"DROP TRIGGER g ON t",
		"ALTER TABLE t ALTER COLUMN c DROP DEFAULT",
		"ALTER TABLE t ALTER COLUMN c DROP NOT NULL",
		"ALTER TABLE t RENAME CONSTRAINT a TO b",
		"ALTER INDEX i RENAME TO j",
		"DROP SCHEMA s",
		"DROP VIEW v",
		"DROP MATERIALIZED VIEW m",
		"DROP POLICY p ON t",
		"DROP PROCEDURE p()",
		"DROP ROUTINE r()",
		"DROP AGGREGATE a(int)",
		"DROP DOMAIN d",
		"ALTER TYPE ty DROP ATTRIBUTE a",
		"ALTER TYPE ty RENAME TO u",
	} {
		if r := scanUp(t, sql+";"); len(r.Triggers) != 0 {
			t.Errorf("%q: got triggers %q", sql, objects(r))
		}
	}
}

func TestComments_AreNotRead(t *testing.T) {
	for _, sql := range []string{
		"-- DROP TABLE t",
		"/* DROP TABLE t */",
		"/* a /* DROP TABLE t */ b */",
		"SELECT 1; -- DROP TABLE t",
	} {
		if r := scanUp(t, sql); len(r.Triggers) != 0 {
			t.Errorf("%q: got triggers %q", sql, objects(r))
		}
	}
}

func TestLexer_DashesInsideAStringAreNotAComment(t *testing.T) {
	r := scanUp(t, "INSERT INTO m VALUES ('a -- b'); DROP TABLE t;")
	if got := objects(r); !slices.Equal(got, []string{"t"}) {
		t.Fatalf("got %q", got)
	}
}

func TestLexer_QuoteEscapes(t *testing.T) {
	for sql, want := range map[string]string{
		`SELECT 'it''s'; DROP TABLE a;`:  "a",
		`SELECT E'it\'s'; DROP TABLE b;`: "b",
	} {
		if got := objects(scanUp(t, sql)); !slices.Equal(got, []string{want}) {
			t.Errorf("%q: got %q", sql, got)
		}
	}
}

func TestTriggers_InsideBodies(t *testing.T) {
	r := scanUp(t, `CREATE FUNCTION fn() RETURNS void AS $$ BEGIN DROP TABLE body; END $$ LANGUAGE plpgsql;
-- +goose StatementBegin
DO $do$ BEGIN IF true THEN DROP TABLE x; END IF; END $do$;
-- +goose StatementEnd
DO $$ BEGIN EXECUTE 'DROP TABLE y'; END $$;`)
	if got := objects(r); !slices.Equal(got, []string{"body", "x", "y"}) {
		t.Fatalf("got %q", got)
	}
}

func TestLexer_CommentInsideADollarBodyIsRead(t *testing.T) {
	r := scanUp(t, "DO $$ BEGIN -- DROP TABLE t\nEND $$;")
	if got := objects(r); !slices.Equal(got, []string{"t"}) {
		t.Fatalf("got %q", got)
	}
	lines, failed := evalUp(t, "DO $$ BEGIN -- contract-step t: why\nDROP TABLE t; END $$;")
	wantLines(t, lines, []string{unmarkedLine(testFile, "DROP TABLE t", "t")})
	if !failed {
		t.Fatal("want failed")
	}
}

func TestLexer_StrayApostropheInDollarQuotedTextFailsClosed(t *testing.T) {
	const comment = "COMMENT ON TABLE t IS $$user's table$$;"
	lines, failed := evalUp(t, comment+" DROP TABLE t; -- contract-step t: gone")
	wantLines(t, lines, []string{"Deliberate schema changes:", testFile + ": t: gone"})
	if failed {
		t.Fatal("(a) want pass")
	}

	lines, failed = evalUp(t, comment+" DROP TABLE t;")
	wantLines(t, lines, []string{unmarkedLine(testFile, "DROP TABLE t", "t")})
	if !failed {
		t.Fatal("(b) want failed")
	}

	body := comment + "\nSELECT 'a -- b'; DROP TABLE t;"
	if got := objects(scanUp(t, body)); !slices.Equal(got, []string{"t"}) {
		t.Fatalf("(c) got %q", got)
	}
	if _, failed = evalUp(t, body); !failed {
		t.Fatal("(c) want failed")
	}

	if _, err := Scan(testFile, upOf("SELECT it's; DROP TABLE t;")); err == nil {
		t.Fatal("(d) want error")
	}
}

func TestLexer_UnterminatedSpanIsAnError(t *testing.T) {
	for _, sql := range []string{
		"SELECT 'abc;",
		`SELECT "abc;`,
		"/* DROP TABLE t;",
		"DO $x$ BEGIN DROP TABLE t; END;",
	} {
		_, err := Scan(testFile, upOf(sql))
		if err == nil || !strings.Contains(err.Error(), testFile) {
			t.Errorf("%q: err = %v", sql, err)
		}
	}
}

func TestLexer_DollarTagRules(t *testing.T) {
	segs, _, err := lex("SELECT $a$ x $b$ DROP TABLE t; $b$ y $a$")
	if err != nil || len(segs) != 1 {
		t.Fatalf("segs %q err %v", segs, err)
	}
	if got := objects(scanUp(t, "SELECT $a$ x $b$ DROP TABLE t; $b$ y $a$")); !slices.Equal(got, []string{"t"}) {
		t.Fatalf("got %q", got)
	}
	for _, sql := range []string{"SELECT foo$bar$", "SELECT $1"} {
		if _, err := Scan(testFile, upOf(sql)); err != nil {
			t.Errorf("%q: %v", sql, err)
		}
	}
}

func TestTriggers_UnparsableDynamicSQLIsNotAHit(t *testing.T) {
	r := scanUp(t, `DO $$ BEGIN EXECUTE format('ALTER TABLE %I DROP COLUMN %I', t, c); END $$;`)
	if len(r.Triggers) != 0 {
		t.Fatalf("got %q", objects(r))
	}
}

func TestMarks_ReadFromRawUpTextNotFromStrippedText(t *testing.T) {
	lines, failed := evalUp(t, "-- contract-step t: replaced by u in 2026-10\nDROP TABLE t;")
	wantLines(t, lines, []string{"Deliberate schema changes:", testFile + ": t: replaced by u in 2026-10"})
	if failed {
		t.Fatal("want pass")
	}
}

func TestMarks_TrailingMarkOnTheStatementLine(t *testing.T) {
	if _, failed := evalUp(t, "DROP TABLE t; -- contract-step t: gone"); failed {
		t.Fatal("want pass")
	}
}

func TestMarks_MatchRules(t *testing.T) {
	for _, c := range []struct{ mark, stmt string }{
		{"T", "DROP TABLE t"},
		{"public.t", "DROP TABLE t"},
		{`"T"`, "DROP TABLE t"},
		{"t.c", "ALTER TABLE public.t DROP COLUMN c"},
		{"public.t", "DROP TABLE public.t"},
	} {
		if _, failed := evalUp(t, "-- contract-step "+c.mark+": why\n"+c.stmt+";"); failed {
			t.Errorf("mark %q should release %q", c.mark, c.stmt)
		}
	}
}

func TestMarks_WrongNameDoesNotRelease(t *testing.T) {
	for _, c := range []struct{ mark, stmt, obj string }{
		{"t", "ALTER TABLE t DROP COLUMN c", "t.c"},
		{"c", "ALTER TABLE t DROP COLUMN c", "t.c"},
		{"other.t", "DROP TABLE public.t", "public.t"},
		{"u", "DROP TABLE t", "t"},
	} {
		lines, failed := evalUp(t, "-- contract-step "+c.mark+": why\n"+c.stmt+";")
		if !failed || !slices.Contains(lines, unmarkedLine(testFile, c.stmt, c.obj)) {
			t.Errorf("mark %q against %q: lines %q", c.mark, c.stmt, lines)
		}
	}
}

func TestMarks_OneMarkReleasesEveryStatementOfTheObject(t *testing.T) {
	lines, failed := evalUp(t, "-- contract-step f: replaced\nDROP FUNCTION f(int); DROP FUNCTION f(text);")
	wantLines(t, lines, []string{"Deliberate schema changes:", testFile + ": f: replaced"})
	if failed {
		t.Fatal("want pass")
	}
}

func TestMarks_FunctionMarkWithArgumentsDoesNotRelease(t *testing.T) {
	lines, failed := evalUp(t, "-- contract-step f(int): why\nDROP FUNCTION f(int);")
	wantLines(t, lines, []string{
		unmarkedLine(testFile, "DROP FUNCTION f(int)", "f"),
		testFile + ": the mark for f(int) names no statement in this Up section. Remove it or correct the name.",
	})
	if !failed {
		t.Fatal("want failed")
	}
}

func noReasonLine(file, obj string) string {
	return file + ": the mark for " + obj + " has no reason. Write why no running revision reads it."
}

func orphanLine(file, obj string) string {
	return file + ": the mark for " + obj + " names no statement in this Up section. Remove it or correct the name."
}

func TestMarks_BlankReasonFails(t *testing.T) {
	for _, mark := range []string{"-- contract-step t:", "-- contract-step t:   ", "-- contract-step t"} {
		lines, failed := evalUp(t, mark+"\nDROP TABLE t;")
		wantLines(t, lines, []string{noReasonLine(testFile, "t")})
		if !failed {
			t.Errorf("%q: want failed", mark)
		}
	}
}

func TestMarks_OrphanFails(t *testing.T) {
	lines, _ := evalUp(t, "-- contract-step u: why\nDROP TABLE t;")
	wantLines(t, lines, []string{unmarkedLine(testFile, "DROP TABLE t", "t"), orphanLine(testFile, "u")})
	lines, _ = evalUp(t, "-- contract-step : why\nDROP TABLE t;")
	wantLines(t, lines, []string{unmarkedLine(testFile, "DROP TABLE t", "t"), orphanLine(testFile, "(empty)")})
}

func TestMarks_OrphanAndBlankReasonReportBoth(t *testing.T) {
	lines, failed := evalUp(t, "-- contract-step u:")
	wantLines(t, lines, []string{noReasonLine(testFile, "u"), orphanLine(testFile, "u")})
	if !failed {
		t.Fatal("want failed")
	}
}

func TestMarks_InDownBlockCommentAndStringReleaseNothing(t *testing.T) {
	for _, sql := range []string{
		"DROP TABLE t;\n-- +goose Down\n-- contract-step t: why",
		"/* contract-step t: why */ DROP TABLE t;",
		"SELECT '-- contract-step t: why'; DROP TABLE t;",
	} {
		lines, failed := Evaluate([]Result{func() Result {
			r, err := Scan(testFile, upOf(sql))
			if err != nil {
				t.Fatal(err)
			}
			return r
		}()})
		wantLines(t, lines, []string{unmarkedLine(testFile, "DROP TABLE t", "t")})
		if !failed {
			t.Errorf("%q: want failed", sql)
		}
	}
}

func TestEvaluate_UnmarkedCopyIsExact(t *testing.T) {
	lines, failed := evalUp(t, "DROP TABLE public.staff_members;")
	want := "migrations/x.sql: DROP TABLE public.staff_members deletes or renames schema that a running revision may read. Write it in a later deploy, or add to the Up section: -- contract-step public.staff_members: <why no running revision reads it>"
	wantLines(t, lines, []string{want})
	if !failed {
		t.Fatal("want failed")
	}
}

func TestEvaluate_DropOfAnObjectTheSameMigrationCreatedStillNeedsAMark(t *testing.T) {
	lines, failed := evalUp(t, "CREATE TABLE tmp (id int); DROP TABLE tmp;")
	wantLines(t, lines, []string{unmarkedLine(testFile, "DROP TABLE tmp", "tmp")})
	if !failed {
		t.Fatal("want failed")
	}
}

func TestEvaluate_ReportsEveryFailureInEveryFile(t *testing.T) {
	scan := func(file, body string) Result {
		r, err := Scan(file, upOf(body))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	b := scan("migrations/b.sql", "-- contract-step z:\nDROP TABLE z;\nDROP TABLE q;\n-- contract-step orphan: why")
	a := scan("migrations/a.sql", "DROP TABLE a1; DROP TABLE a2;")
	lines, failed := Evaluate([]Result{b, a})
	wantLines(t, lines, []string{
		unmarkedLine("migrations/a.sql", "DROP TABLE a1", "a1"),
		unmarkedLine("migrations/a.sql", "DROP TABLE a2", "a2"),
		unmarkedLine("migrations/b.sql", "DROP TABLE q", "q"),
		noReasonLine("migrations/b.sql", "z"),
		orphanLine("migrations/b.sql", "orphan"),
	})
	if !failed {
		t.Fatal("want failed")
	}
}

func TestEvaluate_NoTriggersAnywhereSaysNoDeliberateChanges(t *testing.T) {
	for _, rs := range [][]Result{nil, {scanUp(t, "SELECT 1;")}} {
		lines, failed := Evaluate(rs)
		wantLines(t, lines, []string{"No deliberate schema changes."})
		if failed {
			t.Fatal("want pass")
		}
	}
}

func TestEvaluate_PassWithMarksListsEachMark(t *testing.T) {
	a, _ := Scan("migrations/a.sql", upOf("DROP TABLE a; -- contract-step a: gone a"))
	b, _ := Scan("migrations/b.sql", upOf("DROP TABLE b; -- contract-step b: gone b"))
	lines, failed := Evaluate([]Result{b, a})
	wantLines(t, lines, []string{"Deliberate schema changes:", "migrations/a.sql: a: gone a", "migrations/b.sql: b: gone b"})
	if failed {
		t.Fatal("want pass")
	}
}

func TestAdversarial_FormsThatMustTrigger(t *testing.T) {
	cases := []struct {
		sql  string
		objs []string
	}{
		{"ALTER TABLE t ALTER c DROP NOT NULL, DROP x", []string{"t.x"}},
		{"ALTER TABLE t ALTER COLUMN c SET DEFAULT 'a,b', DROP x", []string{"t.x"}},
		{"ALTER TABLE t DROP CONSTRAINT IF EXISTS c, DROP COLUMN y", []string{"t.y"}},
		{"DROP  \n\t TABLE\n\nIF\nEXISTS\n t", []string{"t"}},
		{"ALTER\nTABLE\n t\nDROP\nCOLUMN\n c", []string{"t.c"}},
		{`ALTER TABLE "a b" RENAME TO c`, []string{"a b"}},
		{`DROP TABLE "we.ird name"`, []string{`"we.ird name"`}},
		{`ALTER TABLE "a b" DROP "c d"`, []string{"a b.c d"}},
		{"ALTER TABLE t DROP c, DROP d", []string{"t.c", "t.d"}},
		{"ALTER TABLE t RENAME COLUMN a TO b, RENAME COLUMN c TO d", []string{"t.a", "t.c"}},
		{"DROP TABLE public.t, s.u CASCADE", []string{"public.t", "s.u"}},
	}
	for _, c := range cases {
		r := scanUp(t, c.sql+";")
		if len(r.Triggers) == 0 || !slices.Equal(objects(r), c.objs) {
			t.Errorf("%q: got %q, want %q", c.sql, objects(r), c.objs)
		}
	}
}

func TestAdversarial_WordsThatLookLikeTriggersGiveNone(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1 AS backdrop_table",
		"SELECT backdrop table_x FROM z",
		"CREATE TABLE my_drop_table (a int)",
		"CREATE TABLE dropoff (drop_type int)",
		"ALTER TABLE t ADD COLUMN drop_c int",
		"ALTER TABLE t DROP CONSTRAINT IF EXISTS c",
		"ALTER TABLE t ALTER c DROP NOT NULL",
	} {
		if r := scanUp(t, sql+";"); len(r.Triggers) != 0 {
			t.Errorf("%q: got triggers %q", sql, objects(r))
		}
	}
}

func TestAdversarial_CRLFAndIndentedGooseLines(t *testing.T) {
	sql := "  -- +goose Up\r\nDROP TABLE t; -- contract-step t: gone\r\n\t-- +goose Down\r\nDROP TABLE u;\r\n"
	r, err := Scan(testFile, sql)
	if err != nil || len(r.Triggers) != 1 || len(r.Marks) != 1 {
		t.Fatalf("got %+v, %v", r, err)
	}
	if r.Marks[0].Reason != "gone" {
		t.Errorf("reason = %q, want gone (no carriage return)", r.Marks[0].Reason)
	}
	lines, failed := Evaluate([]Result{r})
	if failed || len(lines) != 2 {
		t.Errorf("lines %q failed %v", lines, failed)
	}
	r, err = Scan(testFile, "-- +goose Up\r\nALTER TABLE t\r\n  DROP COLUMN c;\r\n")
	if err != nil || !slices.Equal(objects(r), []string{"t.c"}) {
		t.Errorf("CRLF alter: %+v, %v", r, err)
	}
}

func TestAdversarial_MarkBeforeOrAfterItsStatementReleases(t *testing.T) {
	for _, body := range []string{
		"-- contract-step t: why\nDROP TABLE t;",
		"DROP TABLE t;\n-- contract-step t: why",
	} {
		lines, failed := evalUp(t, body)
		if failed || len(lines) != 2 || lines[0] != "Deliberate schema changes:" {
			t.Errorf("%q: lines %q failed %v", body, lines, failed)
		}
	}
}

func TestAdversarial_MarkSpellings(t *testing.T) {
	for _, c := range []struct {
		comment string
		marks   int
	}{
		{"-- contract-stepx t: why", 0},
		{"--contract-step t: why", 1},
		{"--   contract-step   t  :  why  ", 1},
		{"-- Contract-Step t: why", 0},
		{"-- contract-step\tt: why", 1},
	} {
		r := scanUp(t, "SELECT 1;\n"+c.comment)
		if len(r.Marks) != c.marks {
			t.Errorf("%q: %d marks, want %d", c.comment, len(r.Marks), c.marks)
		}
	}
}

func TestAdversarial_NestedCommentAndQuotedCommaDiscriminate(t *testing.T) {
	if r := scanUp(t, "/* a /* b */ DROP TABLE t */ SELECT 1;"); len(r.Triggers) != 0 {
		t.Errorf("nested comment: got %q", objects(r))
	}
	r := scanUp(t, "ALTER TABLE t ADD COLUMN n text DEFAULT 'a, DROP COLUMN z', DROP COLUMN y;")
	if got := objects(r); !slices.Equal(got, []string{"t.y"}) {
		t.Errorf("quoted comma: got %q, want [t.y]", got)
	}
}

func TestAdversarial_StringLiteralsAreRead(t *testing.T) {
	r := scanUp(t, "INSERT INTO audit_log VALUES ('DROP TABLE z');")
	if got := objects(r); !slices.Equal(got, []string{"z"}) {
		t.Fatalf("got %q, want [z]: a literal is read and needs a mark", got)
	}
}

func TestTriggers_ApostropheInADefaultDoesNotHideALaterDrop(t *testing.T) {
	for _, sql := range []string{
		`ALTER TABLE t ADD COLUMN x text DEFAULT $$it's$$, DROP COLUMN y;`,
		`ALTER TABLE t ADD COLUMN x text DEFAULT E'it\'s', DROP COLUMN y;`,
		`ALTER TABLE t ADD COLUMN x text DEFAULT $tag$it's, $$ $tag$, DROP COLUMN y;`,
	} {
		if got := objects(scanUp(t, sql)); !slices.Equal(got, []string{"t.y"}) {
			t.Errorf("%q: got %q", sql, got)
		}
	}
}

func TestTriggers_AlterTableInsideADollarBodyThenALaterStatement(t *testing.T) {
	r := scanUp(t, `DO $$ BEGIN ALTER TABLE a ADD COLUMN x text DEFAULT 'it''s', DROP COLUMN y; END $$;
ALTER TABLE b DROP COLUMN z;`)
	if got := objects(r); !slices.Equal(got, []string{"a.y", "b.z"}) {
		t.Fatalf("got %q", got)
	}
	r = scanUp(t, "DO $$ ALTER TABLE a DROP COLUMN y $$;\nALTER TABLE b DROP COLUMN z;")
	if got := objects(r); !slices.Equal(got, []string{"a.y", "b.z"}) {
		t.Fatalf("no semicolon in body: got %q", got)
	}
}

func TestMarks_ObjectCopiedFromTheMessageWithADotInAQuotedNameReleases(t *testing.T) {
	lines, failed := evalUp(t, `DROP TABLE "a.b";`)
	if !failed || len(lines) != 1 {
		t.Fatalf("want one failure, got %q", lines)
	}
	_, rest, _ := strings.Cut(lines[0], "-- contract-step ")
	obj, _, _ := strings.Cut(rest, ": <why")
	if obj == "" {
		t.Fatalf("no object in %q", lines[0])
	}
	lines, failed = evalUp(t, `DROP TABLE "a.b"; -- contract-step `+obj+`: gone`)
	wantLines(t, lines, []string{"Deliberate schema changes:", testFile + ": " + obj + ": gone"})
	if failed {
		t.Fatal("want pass")
	}
}

func TestAdversarial_QuoteFormsDoNotHideALaterDrop(t *testing.T) {
	for _, d := range []string{
		`x'it''s'`, `X'1F'`, `e'a\\'`, `e'a\'b'`, `U&'d\0061t\+000061'`, `u&'it''s'`,
		`''`, `'a''b'`, `$a$ it's $a$`, `$$ " $$`, `$$$$`, `$t$ $$ ' $t$`, `'$$'`, `"a$b"`, `"$$"`,
		`ne'a'`, `B'101'`, `'a'', DROP COLUMN z'`, `'a''b'`,
	} {
		sql := `ALTER TABLE t ADD COLUMN x text DEFAULT ` + d + `, DROP COLUMN y;`
		if got := objects(scanUp(t, sql)); !slices.Equal(got, []string{"t.y"}) {
			t.Errorf("%s: got %q, want [t.y]", d, got)
		}
	}
	if got := objects(scanUp(t, `ALTER TABLE t DROP COLUMN "a$$b", DROP COLUMN z;`)); !slices.Equal(got, []string{"t.a$$b", "t.z"}) {
		t.Errorf("dollar in quoted identifier: got %q", got)
	}
	if got := objects(scanUp(t, `ALTER TABLE t ADD COLUMN a$b$ int, DROP COLUMN y;`)); !slices.Equal(got, []string{"t.y"}) {
		t.Errorf("dollar after an identifier char: got %q", got)
	}
	if got := objects(scanUp(t, `ALTER TABLE t ADD COLUMN x text DEFAULT 'a', DROP COLUMN y;`)); !slices.Equal(got, []string{"t.y"}) {
		t.Errorf("plain default: got %q", got)
	}
}

func TestTriggers_CommentsBetweenTokensDoNotHideADrop(t *testing.T) {
	cases := []struct {
		sql  string
		objs []string
	}{
		{"ALTER TABLE t\n -- legacy\n DROP COLUMN x", []string{"t.x"}},
		{"ALTER TABLE t ADD y int, -- add\n DROP COLUMN x", []string{"t.x"}},
		{"DROP /*c*/ TABLE t", []string{"t"}},
		{"ALTER TABLE t ADD y int -- don't\n , DROP COLUMN x", []string{"t.x"}},
		{"ALTER TABLE t ADD y int /* a, b */, DROP COLUMN x", []string{"t.x"}},
	}
	for _, c := range cases {
		for _, sql := range []string{c.sql + ";", "DO $$ BEGIN " + c.sql + "; END $$;"} {
			if got := objects(scanUp(t, sql)); !slices.Equal(got, c.objs) {
				t.Errorf("%q: got %q, want %q", sql, got, c.objs)
			}
		}
	}
}

func TestTriggers_OddTokensDoNotHideADrop(t *testing.T) {
	cases := []struct {
		sql  string
		objs []string
	}{
		{"ALTER TABLE t * DROP COLUMN x", []string{"t.x"}},
		{"ALTER TABLE public . t DROP COLUMN x", []string{"public.t.x"}},
		{`ALTER TABLE "a""b" DROP COLUMN x`, []string{`"a""b".x`}},
		{"DROP TABLE\ffoo", []string{"foo"}},
	}
	for _, c := range cases {
		if got := objects(scanUp(t, c.sql+";")); !slices.Equal(got, c.objs) {
			t.Errorf("%q: got %q, want %q", c.sql, got, c.objs)
		}
	}
}

func TestMarks_QuotedNameWithADoubledQuoteReleases(t *testing.T) {
	if lines, failed := evalUp(t, `-- contract-step "a""b".x: why`+"\n"+`ALTER TABLE "a""b" DROP COLUMN x;`); failed {
		t.Fatalf("lines %q", lines)
	}
}

func TestTriggers_UnplacedDropOrRenameWordInAlterTableFailsClosed(t *testing.T) {
	for _, sql := range []string{
		"ALTER TABLE t DROP COLUMN %I",
		"ALTER TABLE t FROB DROP COLUMN x",
		"ALTER TABLE t ADD y int, FROB RENAME x TO z",
	} {
		if got := objects(scanUp(t, sql+";")); !slices.Equal(got, []string{"t"}) {
			t.Errorf("%q: got %q, want the table", sql, got)
		}
	}
	for _, sql := range []string{
		"ALTER TABLE t ADD y text DEFAULT 'drop it'",
		"ALTER TABLE t ADD y int -- drop later\n",
		"ALTER TABLE t ALTER COLUMN c DROP IDENTITY",
	} {
		if got := objects(scanUp(t, sql+";")); len(got) != 0 {
			t.Errorf("%q: got %q, want none", sql, got)
		}
	}
}

func TestMarks_OneMarkClaimsOneObject(t *testing.T) {
	cases := []struct {
		name, sql string
		want      []string
	}{
		{"kinds", "-- contract-step foo: why\nDROP TABLE foo; DROP FUNCTION foo;", []string{unmarkedLine(testFile, "DROP FUNCTION foo", "foo")}},
		{"two marks", "-- contract-step foo: a\n-- contract-step foo: b\nDROP TABLE foo; DROP FUNCTION foo;", nil},
		{"schemas", "-- contract-step foo: why\nDROP TABLE a.foo; DROP TABLE b.foo;", []string{unmarkedLine(testFile, "DROP TABLE b.foo", "b.foo")}},
		{"column and table", "-- contract-step orders.legacy: why\nALTER TABLE orders DROP COLUMN legacy; DROP TABLE legacy;", []string{unmarkedLine(testFile, "DROP TABLE legacy", "legacy")}},
	}
	for _, c := range cases {
		lines, failed := evalUp(t, c.sql)
		if c.want == nil {
			if failed || len(lines) != 3 || lines[0] != "Deliberate schema changes:" {
				t.Errorf("%s: lines %q failed %v", c.name, lines, failed)
			}
			continue
		}
		if !failed || !slices.Equal(lines, c.want) {
			t.Errorf("%s:\n got: %q\nwant: %q", c.name, lines, c.want)
		}
	}
}

func TestTriggers_PostgresWhitespaceAndLineEndsHideNoDrop(t *testing.T) {
	cases := []struct {
		sql  string
		objs []string
	}{
		{"DROP\vTABLE\vfoo", []string{"foo"}},
		{"ALTER\vTABLE\vt\vDROP\vCOLUMN\vx", []string{"t.x"}},
		{"SELECT 1; -- note\rDROP TABLE t", []string{"t"}},
		{"ALTER TABLE t ADD y int -- note\r, DROP COLUMN x", []string{"t.x"}},
	}
	for _, c := range cases {
		if got := objects(scanUp(t, c.sql+";")); !slices.Equal(got, c.objs) {
			t.Errorf("%q: got %q, want %q", c.sql, got, c.objs)
		}
	}
}

func TestMarks_QuotedMixedCaseNameIsAnotherObject(t *testing.T) {
	_, failed := evalUp(t, "-- contract-step foo: why\nDROP TABLE foo; DROP TABLE \"Foo\";")
	if !failed {
		t.Fatal("one mark for foo released the drop of \"Foo\", a different table")
	}
}

func TestMarks_MarkIgnoresCaseOfAQuotedName(t *testing.T) {
	if _, failed := evalUp(t, "-- contract-step foo: why\nDROP TABLE \"Foo\";"); failed {
		t.Fatal("a mark for foo must release the drop of \"Foo\": mark matching ignores case")
	}
}

func TestUpSection_ReadsGooseAnnotationsLikeGoose(t *testing.T) {
	r, err := Scan(testFile, "-- +goose up\nSELECT 1;\n-- +goose down\nDROP TABLE t;\n")
	if err != nil || len(r.Triggers) != 0 {
		t.Fatalf("down section read: %+v, %v", r, err)
	}
	for _, up := range []string{"-- +goose up", "--+goose Up"} {
		r, err := Scan(testFile, up+"\nDROP TABLE t;\n")
		if err != nil || len(r.Triggers) != 1 {
			t.Errorf("%q: %+v, %v", up, r, err)
		}
	}
}
