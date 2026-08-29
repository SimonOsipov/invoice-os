// AUDIT-04-11 file-only drift fences for "the scoped per-invoice read" (System Design
// §6): the new audit_log.invoice_id generated expression must dispatch on exactly the
// same two event lists, and use exactly the same uuid-gate grammar, as the live
// audit_log_entity_for resolver. No DB needed — these run unconditionally, like the two
// migrations.FS cases in audit_schema_test.go.
package audit_test

import (
	"io/fs"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const scopedInvoiceIDMigrationGlob = "*_audit_log_invoice_id_column_and_index.sql"

const (
	scopedGooseUp   = "-- +goose Up"
	scopedGooseDown = "-- +goose Down"
)

// scopedStripComments drops `--` line comments that sit outside a single-quoted literal,
// so header/body prose can't accidentally satisfy or trip a literal-text scan.
func scopedStripComments(sql string) string {
	var b strings.Builder
	for i := 0; i < len(sql); {
		switch {
		case sql[i] == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case sql[i] == '\'':
			b.WriteByte(sql[i])
			i++
			for i < len(sql) {
				if sql[i] == '\'' {
					if i+1 < len(sql) && sql[i+1] == '\'' {
						b.WriteString("''")
						i += 2
						continue
					}
					b.WriteByte(sql[i])
					i++
					break
				}
				b.WriteByte(sql[i])
				i++
			}
		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	return b.String()
}

func scopedGooseUpOf(raw string) (string, bool) {
	up := strings.Index(raw, scopedGooseUp)
	down := strings.Index(raw, scopedGooseDown)
	if up < 0 || down < 0 || down < up {
		return "", false
	}
	return raw[up+len(scopedGooseUp) : down], true
}

// scopedInvoiceIDMigrationUp returns the story migration's comment-stripped Up body. The
// glob match failing here (0 files pre-implementation) is the expected RED for this file.
func scopedInvoiceIDMigrationUp(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, scopedInvoiceIDMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", scopedInvoiceIDMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d files matching %s (%v), want exactly 1",
			len(matches), scopedInvoiceIDMigrationGlob, matches)
	}
	raw, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s from migrations.FS: %v", matches[0], err)
	}
	up, ok := scopedGooseUpOf(string(raw))
	if !ok {
		t.Fatalf("%s: want both %q and %q, in that order", matches[0], scopedGooseUp, scopedGooseDown)
	}
	return scopedStripComments(up)
}

// scopedResolverDefRE finds a CREATE [OR REPLACE] FUNCTION audit_log_entity_for( — the
// parameter list terminates the identifier, so a renamed function no longer counts.
var scopedResolverDefRE = regexp.MustCompile(
	`(?is)CREATE\s+(OR\s+REPLACE\s+)?FUNCTION\s+([a-z_][a-z0-9_$]*\s*\.\s*)?audit_log_entity_for\s*\(`)

// scopedResolverDefinerBody returns the comment-stripped Up section of the LAST migration
// (by filename sort — goose applies files in that order) whose body defines
// audit_log_entity_for. That is the definition actually live in the database today.
func scopedResolverDefinerBody(t *testing.T) string {
	t.Helper()
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	if len(names) == 0 {
		t.Fatalf("migrations.FS contains no *.sql files — the embed is broken")
	}
	sort.Strings(names)

	var definer, body string
	for _, name := range names {
		raw, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatalf("read %s from migrations.FS: %v", name, err)
		}
		up, ok := scopedGooseUpOf(string(raw))
		if !ok {
			continue // no goose markers: not a migration body we can read
		}
		if scopedResolverDefRE.MatchString(scopedStripComments(up)) {
			definer, body = name, up
		}
	}
	if definer == "" {
		t.Fatalf("no migration in migrations.FS defines audit_log_entity_for")
	}
	return scopedStripComments(body)
}

// scopedQuotedLiteralRE matches a single-quoted, dotted lowercase literal — an event name
// shape ('invoice.created', 'reconciliation.drift_detected'). Payload-key literals like
// 'id' and 'invoice_id' carry no dot, so they never match.
var scopedQuotedLiteralRE = regexp.MustCompile(`'([a-z_]+(?:\.[a-z_]+)+)'`)

func scopedQuotedEventNames(t *testing.T, listText string) []string {
	t.Helper()
	matches := scopedQuotedLiteralRE.FindAllStringSubmatch(listText, -1)
	if len(matches) == 0 {
		t.Fatalf("event list %q holds no dotted event-name literal", listText)
	}
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m[1]
	}
	return names
}

// scopedEventInRE finds an `event IN (...)` branch condition, SQL-CASE style (the
// migration's dispatch) rather than the resolver's PL/pgSQL `p_event IN (...)`.
var scopedEventInRE = regexp.MustCompile(`(?is)event\s+IN\s*\(([^)]*)\)`)

// scopedEventListsFromExpression extracts the two dispatch event lists from the
// generated column's CASE expression, keyed by which payload key its THEN-clause reads —
// not by branch order, since the migration is free to write either branch first.
func scopedEventListsFromExpression(t *testing.T, body string) (idEvents, invoiceIDEvents []string) {
	t.Helper()
	locs := scopedEventInRE.FindAllStringSubmatchIndex(body, -1)
	if len(locs) != 2 {
		t.Fatalf("the generated expression holds %d `event IN (...)` branches, want exactly 2 "+
			"(one per payload spelling)", len(locs))
	}
	for i, loc := range locs {
		listText := body[loc[2]:loc[3]]
		thenEnd := len(body)
		if i+1 < len(locs) {
			thenEnd = locs[i+1][0]
		}
		thenText := body[loc[1]:thenEnd]
		names := scopedQuotedEventNames(t, listText)
		switch {
		case strings.Contains(thenText, "->>'invoice_id'"):
			invoiceIDEvents = append(invoiceIDEvents, names...)
		case strings.Contains(thenText, "->>'id'"):
			idEvents = append(idEvents, names...)
		default:
			t.Fatalf("branch %d's THEN clause names neither payload->>'id' nor "+
				"payload->>'invoice_id': %q", i, thenText)
		}
	}
	return idEvents, invoiceIDEvents
}

// scopedResolverEventLists returns the resolver's rule-A (bare id) and rule-B
// (invoice_id spelling) event lists, in that order — the live resolver's own structure
// (migrations/20260821135423_...sql), verified by reading its shipped Up body. A third
// `p_event IN (...)` branch (rule C, portfolio.entity.*, also id-keyed) may follow and is
// deliberately not consulted.
func scopedResolverEventLists(t *testing.T, body string) (idEvents, invoiceIDEvents []string) {
	t.Helper()
	re := regexp.MustCompile(`(?is)p_event\s+IN\s*\(([^)]*)\)`)
	locs := re.FindAllStringSubmatchIndex(body, -1)
	if len(locs) < 2 {
		t.Fatalf("the resolver body holds %d `p_event IN (...)` branches, want at least 2 "+
			"(rule A and rule B)", len(locs))
	}
	idEvents = scopedQuotedEventNames(t, body[locs[0][2]:locs[0][3]])
	invoiceIDEvents = scopedQuotedEventNames(t, body[locs[1][2]:locs[1][3]])
	return idEvents, invoiceIDEvents
}

// Row 8 / AC-8: the migration's two dispatch lists are set-equal to the live resolver's
// rule-A and rule-B lists — 10 and 7 — so the two cannot silently drift apart.
func TestAudit_GeneratedInvoiceIDListsMatchTheLiveResolver(t *testing.T) {
	migID, migInvoiceID := scopedEventListsFromExpression(t, scopedInvoiceIDMigrationUp(t))
	resID, resInvoiceID := scopedResolverEventLists(t, scopedResolverDefinerBody(t))

	for name, list := range map[string][]string{
		"migration id-branch":         migID,
		"migration invoice_id-branch": migInvoiceID,
		"resolver id-branch":          resID,
		"resolver invoice_id-branch":  resInvoiceID,
	} {
		if len(list) == 0 {
			t.Fatalf("%s event list is empty", name)
		}
	}

	sorted := func(s []string) []string {
		out := slices.Clone(s)
		sort.Strings(out)
		return out
	}
	if got, want := sorted(migID), sorted(resID); !slices.Equal(got, want) {
		t.Errorf("migration id-branch events = %v, want set-equal to the resolver's %v", got, want)
	}
	if got, want := sorted(migInvoiceID), sorted(resInvoiceID); !slices.Equal(got, want) {
		t.Errorf("migration invoice_id-branch events = %v, want set-equal to the resolver's %v", got, want)
	}
	if len(migID) != 10 {
		t.Errorf("migration id-branch holds %d events, want 10", len(migID))
	}
	if len(migInvoiceID) != 7 {
		t.Errorf("migration invoice_id-branch holds %d events, want 7", len(migInvoiceID))
	}
}

// scopedGrammarLiteralRE matches the uuid-gate grammar's quoted literal content, e.g.
// '^[0-9a-f]{4}(-?[0-9a-f]{4}){7}$'. Non-greedy up to the pattern's own single '$'
// end-anchor, which is the only '$' the literal contains.
var scopedGrammarLiteralRE = regexp.MustCompile(`(?s)'(\^\[0-9a-f\]\{4\}.*?\$)'`)

// Row 9 (new) / AC-2: the migration's two grammar copies are byte-identical to the live
// resolver's — contract §6's "change one, change all". Eight copies across the repo today;
// count them from the tree, not from this line.
func TestAudit_GeneratedInvoiceIDGrammarIsByteIdenticalToTheResolver(t *testing.T) {
	migMatches := scopedGrammarLiteralRE.FindAllStringSubmatch(scopedInvoiceIDMigrationUp(t), -1)
	if len(migMatches) != 2 {
		t.Fatalf("the migration holds %d copies of the uuid-gate grammar, want exactly 2 "+
			"(one per dispatch branch)", len(migMatches))
	}

	resMatches := scopedGrammarLiteralRE.FindAllStringSubmatch(scopedResolverDefinerBody(t), -1)
	if len(resMatches) == 0 {
		t.Fatalf("the resolver body holds no uuid-gate grammar literal — the extraction pattern is broken")
	}
	want := resMatches[0][1]
	if want == "" {
		t.Fatalf("the resolver's extracted grammar literal is empty")
	}

	for i, m := range migMatches {
		if m[1] != want {
			t.Errorf("migration grammar copy %d = %q, want byte-identical to the resolver's %q", i, m[1], want)
		}
	}
}
