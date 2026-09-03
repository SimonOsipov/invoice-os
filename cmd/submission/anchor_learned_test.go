// anchor_learned_test.go: what the rule-learning audit adapter writes, and where its event is
// SPELLED. Nothing here serves a mux or opens a database; the adapter is driven directly.
//
// Helpers use an al* prefix; fc dr ea wt ds up pgi li are taken.
package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	alEvent     = "extraction.anchor.learned"
	alAdapterFn = "newAnchorLearnedAuditor"

	// Sentinels the payload must NOT carry. Each is what a widened AnchorLearned would leak:
	// the corrected value, the anchor text read verbatim off the document, and a box edge.
	alValueSentinel  = "ZQXCORRECTEDVALUE0001"
	alAnchorSentinel = "ZQXANCHORTEXT0002"
	alBoxSentinel    = "0.13570246"

	alInvoiceID   = "5d2f7a10-6b3c-4e8d-9f01-2a3b4c5d6e7f"
	alFingerprint = "9f4b1c0e2d3a5b6c7d8e9f0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e"
	alRuleID      = "7c8d9e0f-1a2b-4c3d-8e4f-5a6b7c8d9e0f"
	alActor       = "e5b10007-0000-4000-8000-000000000001"
)

// alSixKeys is the payload's COMPLETE key set, sorted -- eaRow.keys() sorts. Asserted as a
// whole: a seventh key fails and a missing key fails, which is what makes this the leak pin
// rather than a restatement of the six assignments.
var alSixKeys = []string{
	"anchor_rule_id", "field", "invoice_id", "layout_fingerprint", "relation", "shape",
}

// The payload the resolver dispatches on, and the leak guard. Driving the REAL adapter is the
// only oracle for the key spelling: internal/extraction's DB suite injects a recorder of its
// own, so a production adapter writing "id" instead passes every test there while every row
// resolves entity_id NULL.
func TestNewAnchorLearnedAuditor_WritesExactlyTheSixKeys(t *testing.T) {
	tx := &eaTx{}
	a := extraction.AnchorLearned{
		InvoiceID:         alInvoiceID,
		FieldName:         "buyer_tin",
		LayoutFingerprint: alFingerprint,
		RuleID:            alRuleID,
		Relation:          extraction.RelSameToken,
		Shape:             extraction.ShapeTIN,
	}

	if err := newAnchorLearnedAuditor()(context.Background(), tx, alActor, a); err != nil {
		t.Fatalf("the recorder returned %v, want nil", err)
	}
	row := eaDecodeOne(t, tx)

	if row.actor != alActor {
		t.Errorf("the row names actor %q, want the caller's own subject %q", row.actor, alActor)
	}
	if row.event != alEvent {
		t.Errorf("the row carries event %q, want %q", row.event, alEvent)
	}
	if got := row.keys(); !reflect.DeepEqual(got, alSixKeys) {
		t.Errorf("the row carries keys %v, want exactly %v -- audit_log_set_entity dispatches this "+
			"event on payload->>'invoice_id', and every other key is closed vocabulary or a hash",
			got, alSixKeys)
	}
	want := map[string]any{
		"invoice_id":         alInvoiceID,
		"field":              "buyer_tin",
		"layout_fingerprint": alFingerprint,
		"anchor_rule_id":     alRuleID,
		"relation":           string(extraction.RelSameToken),
		"shape":              string(extraction.ShapeTIN),
	}
	if !reflect.DeepEqual(row.payload, want) {
		t.Errorf("the row carries payload %v, want exactly %v", row.payload, want)
	}
}

// The leak half, run against a struct whose every string field carries a sentinel the payload
// has no key for. A widened AnchorLearned plus a widened adapter in one commit is what this
// catches; the key-set pin above catches only the second half.
func TestNewAnchorLearnedAuditor_CarriesNoValueNoAnchorTextAndNoBox(t *testing.T) {
	tx := &eaTx{}
	a := extraction.AnchorLearned{
		InvoiceID:         alInvoiceID,
		FieldName:         "buyer_tin",
		LayoutFingerprint: alFingerprint + alValueSentinel + alAnchorSentinel + alBoxSentinel,
		RuleID:            alRuleID,
		Relation:          extraction.RelSameToken,
		Shape:             extraction.ShapeTIN,
	}
	if err := newAnchorLearnedAuditor()(context.Background(), tx, alActor, a); err != nil {
		t.Fatalf("the recorder returned %v, want nil", err)
	}
	// Control needle: the sentinels ARE reachable through a field the payload does carry, so a
	// scan that finds none of them below is reading a real payload rather than an empty one.
	raw := alPayloadJSON(t, tx)
	for _, s := range []string{alValueSentinel, alAnchorSentinel, alBoxSentinel} {
		if !strings.Contains(raw, s) {
			t.Fatalf("control needle: %q is absent from a payload built to carry it (%s) -- the scan "+
				"below reads nothing", s, raw)
		}
	}

	tx2 := &eaTx{}
	b := extraction.AnchorLearned{
		InvoiceID:         alInvoiceID,
		FieldName:         "buyer_tin",
		LayoutFingerprint: alFingerprint,
		RuleID:            alRuleID,
		Relation:          extraction.RelSameToken,
		Shape:             extraction.ShapeTIN,
	}
	if err := newAnchorLearnedAuditor()(context.Background(), tx2, alActor, b); err != nil {
		t.Fatalf("the recorder returned %v, want nil", err)
	}
	clean := alPayloadJSON(t, tx2)
	for _, forbidden := range []string{"value", "anchor_text", "text", "label", "region", "bbox", "page", "x0", "y0"} {
		if strings.Contains(clean, `"`+forbidden+`"`) {
			t.Errorf("the payload carries a %q key (%s) -- audit_log is append-only and the corrected "+
				"value, the anchor text and the box are all business content", forbidden, clean)
		}
	}
}

// The event literal belongs in cmd/, never in internal/extraction: a const identifier inside
// that package reads as a non-literal to the repo-wide audit.Record scan and lands the call
// site in no bucket -- which is also what the frontend vocabulary scan reads.
func TestNewAnchorLearnedAuditor_SpellsTheEventInCmd(t *testing.T) {
	root, files := eaProdFiles(t)
	extractionFiles := drExtractionFiles(t, files)

	// Control needle: the literal matcher must find a literal that IS in internal/extraction, or
	// the zero-hit clearance below is a broken walk reporting an empty set.
	if got := eaLiteralSites(t, root, extractionFiles, drNeedleLiteral); len(got) != 1 {
		t.Fatalf("the literal matcher found %q at %v, want exactly 1 site -- the matcher is broken", drNeedleLiteral, got)
	}

	sites := eaLiteralSites(t, root, files, alEvent)
	var inCmd []string
	for _, s := range sites {
		if strings.HasPrefix(s, "cmd/submission/") {
			inCmd = append(inCmd, s)
		}
	}
	if len(inCmd) == 0 {
		t.Errorf("%q is spelled as a production literal at %v, none of them under cmd/submission -- %s is the composition root's job",
			alEvent, sites, alAdapterFn)
	}
	if got := eaLiteralSites(t, root, extractionFiles, alEvent); len(got) != 0 {
		t.Errorf("%q is spelled as a production literal under %s at %v -- a literal there drops the call site out of the repo-wide audit.Record partition",
			alEvent, drExtractionD, got)
	}
}

// alPayloadJSON re-encodes the one row the adapter wrote, so a substring scan reads the bytes
// audit_log stores rather than a Go map's print form.
func alPayloadJSON(t *testing.T, tx *eaTx) string {
	t.Helper()
	raw, err := json.Marshal(eaDecodeOne(t, tx).payload)
	if err != nil {
		t.Fatalf("re-encode the payload: %v", err)
	}
	return string(raw)
}
