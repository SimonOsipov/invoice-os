package archive

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// manifestFormat never says "signed" (D-11: this is a checksum list, not a
// cryptographic signature).
const manifestFormat = "ascomply-evidence-bundle/1"

type manifestActor struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type manifestEntity struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	TIN  *string `json:"tin"` // nil -> JSON null (D-8)
}

type manifestPeriod struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Bounds string `json:"bounds"`
	Basis  string `json:"basis"`
}

type manifestCounts struct {
	Invoices          int `json:"invoices"`
	StatusTransitions int `json:"status_transitions"`
	Submissions       int `json:"submissions"`
	ExchangeAttempts  int `json:"exchange_attempts"`
	BodyFiles         int `json:"body_files"`
}

// manifestEntry: Rows is nil for a body file, a non-nil pointer (even to 0) for a CSV.
type manifestEntry struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	Rows   *int   `json:"rows,omitempty"`
}

// manifestDoc.Entries must never be nil -- see TestManifest_EntriesNeverMarshalsNull.
type manifestDoc struct {
	Format      string          `json:"format"`
	GeneratedAt string          `json:"generated_at"`
	GeneratedBy manifestActor   `json:"generated_by"`
	TenantID    string          `json:"tenant_id"`
	Entity      manifestEntity  `json:"entity"`
	Period      manifestPeriod  `json:"period"`
	Counts      manifestCounts  `json:"counts"`
	Entries     []manifestEntry `json:"entries"`
	Notes       []string        `json:"notes"`
}

// manifestNotes: fixed, non-field-name disclosure of what an empty cell means and
// (D-11, user-decided 2026-08-22) that the SHA-256 list is a checksum, not a signature.
var manifestNotes = []string{
	"An empty CSV cell means the source column was NULL.",
	"irn, csid and qr_payload cannot hold an empty string (database CHECK), so an empty cell there means absent.",
	"Body files are the bytes recorded at transmission time, verbatim. A row whose truncated or encoding_coerced flag is true carries a body that is not the complete wire bytes.",
	"Request and response headers are limited to a twelve-name allowlist applied when the evidence was written; credential headers were never stored.",
	"This manifest lists a SHA-256 checksum for each entry, for self-verification. It is not a cryptographic signature.",
}

// ManifestParams is everything writeManifest needs beyond bw's own recorded entries.
type ManifestParams struct {
	TenantID    string
	Entity      Entity
	Request     Request
	GeneratedBy manifestActor
	Now         time.Time
}

// entryCount reads a CSV's row count back off bw.entries -- no second pass over
// the database. Zero for a name with no recorded rows (e.g. a body file).
func entryCount(entries []manifestEntry, name string) int {
	for _, e := range entries {
		if e.Name == name && e.Rows != nil {
			return *e.Rows
		}
	}
	return 0
}

// bodyFileCount counts entries under the bodies/ prefix.
func bodyFileCount(entries []manifestEntry) int {
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name, "bodies/") {
			n++
		}
	}
	return n
}

// bundleEntity renders e as the manifest/preview wire shape (D-49) -- the single home
// for entity rendering, so writeManifest and Preview cannot drift apart.
func bundleEntity(e Entity) manifestEntity {
	return manifestEntity{ID: e.ID, Name: e.Name, TIN: e.TIN}
}

// bundlePeriod renders r as the manifest/preview wire shape (D-49) -- the single home
// for period rendering, so writeManifest and Preview cannot drift apart.
func bundlePeriod(r Request) manifestPeriod {
	return manifestPeriod{
		From:   r.From.UTC().Format(time.RFC3339),
		To:     r.To.UTC().Format(time.RFC3339),
		Bounds: "inclusive",
		Basis:  "invoices.created_at",
	}
}

// writeManifest builds manifest.json from bw's already-recorded entries and
// writes it as the final ZIP entry. It never appends an entry describing
// itself -- there is no chicken-and-egg problem because bw.entries is read
// before this entry is created.
func (bw *bundleWriter) writeManifest(p ManifestParams) error {
	doc := manifestDoc{
		Format:      manifestFormat,
		GeneratedAt: p.Now.UTC().Format(time.RFC3339),
		GeneratedBy: p.GeneratedBy,
		TenantID:    p.TenantID,
		Entity:      bundleEntity(p.Entity),
		Period:      bundlePeriod(p.Request),
		Counts: manifestCounts{
			Invoices:          entryCount(bw.entries, "invoices.csv"),
			StatusTransitions: entryCount(bw.entries, "status_history.csv"),
			Submissions:       entryCount(bw.entries, "submissions.csv"),
			ExchangeAttempts:  entryCount(bw.entries, "exchange.csv"),
			BodyFiles:         bodyFileCount(bw.entries),
		},
		Entries: bw.entries,
		Notes:   manifestNotes,
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("archive: marshal manifest.json: %w", err)
	}
	w, err := bw.zw.Create("manifest.json")
	if err != nil {
		return fmt.Errorf("archive: create manifest.json entry: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("archive: write manifest.json entry: %w", err)
	}
	return nil
}
