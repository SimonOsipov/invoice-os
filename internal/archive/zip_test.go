// zip_test.go: RED specs for AUDIT-05-06 (Mode A) -- bundleWriter/manifest against a
// real archive/zip, no DB needed. package archive (white-box), matching exchange_test.go.
package archive

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- fixtures and reader helpers ---------------------------------------------------

// fixtureRow builds a data record whose length always matches header, tagged with
// name so a misaligned field is obvious from the value itself.
func fixtureRow(header []string, tag string) []string {
	row := make([]string, len(header))
	for i, col := range header {
		row[i] = tag + ":" + col
	}
	return row
}

func mustReadZip(t *testing.T, raw []byte) *zip.Reader {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	return r
}

func entryNames(r *zip.Reader) []string {
	names := make([]string, len(r.File))
	for i, f := range r.File {
		names[i] = f.Name
	}
	return names
}

func entryIndex(t *testing.T, r *zip.Reader, name string) int {
	t.Helper()
	for i, f := range r.File {
		if f.Name == name {
			return i
		}
	}
	t.Fatalf("archive has no entry %q (entries: %v)", name, entryNames(r))
	return -1
}

func readEntry(t *testing.T, r *zip.Reader, name string) []byte {
	t.Helper()
	rc, err := r.File[entryIndex(t, r, name)].Open()
	if err != nil {
		t.Fatalf("open %q: %v", name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %q: %v", name, err)
	}
	return data
}

func readManifest(t *testing.T, r *zip.Reader) manifestDoc {
	t.Helper()
	raw := readEntry(t, r, "manifest.json")
	var doc manifestDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal manifest.json: %v\nraw: %s", err, raw)
	}
	return doc
}

// testManifestParams is a minimal, valid ManifestParams -- content doesn't matter to
// these tests, only that writeManifest accepts it.
func testManifestParams() ManifestParams {
	return ManifestParams{
		TenantID:    "11111111-1111-1111-1111-111111111111",
		Entity:      Entity{ID: "22222222-2222-2222-2222-222222222222", Name: "Fixture Co"},
		Request:     Request{EntityID: "22222222-2222-2222-2222-222222222222", From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC)},
		GeneratedBy: manifestActor{Name: "QA Fixture", Kind: "system"},
		Now:         time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC),
	}
}

// fixtureBundle is a finished archive plus the inputs used to build it, so tests can
// assert the read-back matches what went in.
type fixtureBundle struct {
	zipBytes []byte
	bodies   map[string][]byte
}

// buildFixtureBundle drives the real bundleWriter through all four CSV stages plus
// two exchange bodies, in D-31's physical order: three CSVs, then bodies streamed
// live, then exchange.csv finalized last, then the manifest.
func buildFixtureBundle(t *testing.T) fixtureBundle {
	t.Helper()
	var buf bytes.Buffer
	bw := newBundleWriter(&buf)

	for _, spec := range []struct {
		name   string
		header []string
	}{
		{"invoices.csv", invoicesCSVHeader},
		{"status_history.csv", historyCSVHeader},
		{"submissions.csv", submissionsCSVHeader},
	} {
		e := bw.newCSVEntry(spec.name)
		if err := e.Write(spec.header); err != nil {
			t.Fatalf("write %s header: %v", spec.name, err)
		}
		if err := e.Write(fixtureRow(spec.header, spec.name)); err != nil {
			t.Fatalf("write %s row: %v", spec.name, err)
		}
		if err := bw.finalizeCSV(e); err != nil {
			t.Fatalf("finalize %s: %v", spec.name, err)
		}
	}

	bodies := map[string][]byte{
		"bodies/ex-1.request":  []byte("request body with \"quotes\"\nand a newline\tand a tab"),
		"bodies/ex-1.response": []byte("response body, plain"),
	}

	ee := bw.newCSVEntry("exchange.csv")
	if err := ee.Write(exchangeCSVHeader); err != nil {
		t.Fatalf("write exchange.csv header: %v", err)
	}
	if err := bw.WriteBody("bodies/ex-1.request", bodies["bodies/ex-1.request"]); err != nil {
		t.Fatalf("write request body: %v", err)
	}
	if err := bw.WriteBody("bodies/ex-1.response", bodies["bodies/ex-1.response"]); err != nil {
		t.Fatalf("write response body: %v", err)
	}
	exRow := fixtureRow(exchangeCSVHeader, "exchange.csv")
	exRow[colIndex(t, exchangeCSVHeader, "request_body_file")] = "bodies/ex-1.request"
	exRow[colIndex(t, exchangeCSVHeader, "response_body_file")] = "bodies/ex-1.response"
	if err := ee.Write(exRow); err != nil {
		t.Fatalf("write exchange.csv row: %v", err)
	}
	if err := bw.finalizeCSV(ee); err != nil {
		t.Fatalf("finalize exchange.csv: %v", err)
	}

	if err := bw.writeManifest(testManifestParams()); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	if err := bw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	return fixtureBundle{zipBytes: buf.Bytes(), bodies: bodies}
}

// --- AC-1: no io.Seeker required ----------------------------------------------------

// writeOnly hides Seek/WriteAt so the writer under test provably needs neither.
type writeOnly struct{ w io.Writer }

func (wo *writeOnly) Write(p []byte) (int, error) { return wo.w.Write(p) }

func TestBundleWriter_WritesToANonSeekableWriter(t *testing.T) {
	var buf bytes.Buffer
	bw := newBundleWriter(&writeOnly{w: &buf})

	e := bw.newCSVEntry("invoices.csv")
	if err := e.Write(invoicesCSVHeader); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := e.Write(fixtureRow(invoicesCSVHeader, "row")); err != nil {
		t.Fatalf("write row: %v", err)
	}
	if err := bw.finalizeCSV(e); err != nil {
		t.Fatalf("finalizeCSV: %v", err)
	}
	if err := bw.writeManifest(testManifestParams()); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	if err := bw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len())); err != nil {
		t.Fatalf("zip.NewReader over a bundle built on a non-seekable writer: %v", err)
	}
}

// --- AC-2: declared headers and field counts ----------------------------------------

func TestBundle_EveryCSVFirstRecordIsItsDeclaredHeader(t *testing.T) {
	fb := buildFixtureBundle(t)
	r := mustReadZip(t, fb.zipBytes)

	headers := map[string][]string{
		"invoices.csv":       invoicesCSVHeader,
		"status_history.csv": historyCSVHeader,
		"submissions.csv":    submissionsCSVHeader,
		"exchange.csv":       exchangeCSVHeader,
	}
	for name, want := range headers {
		rows := parseCSV(t, readEntry(t, r, name))
		if len(rows) == 0 {
			t.Fatalf("%s has no records", name)
		}
		if !slices.Equal(rows[0], want) {
			t.Errorf("%s first record = %v, want the declared header %v", name, rows[0], want)
		}
	}
}

func TestBundle_EveryCSVRecordHasTheHeaderFieldCount(t *testing.T) {
	fb := buildFixtureBundle(t)
	r := mustReadZip(t, fb.zipBytes)

	headers := map[string][]string{
		"invoices.csv":       invoicesCSVHeader,
		"status_history.csv": historyCSVHeader,
		"submissions.csv":    submissionsCSVHeader,
		"exchange.csv":       exchangeCSVHeader,
	}
	checked := 0
	for name, header := range headers {
		rows := parseCSV(t, readEntry(t, r, name)) // ReadAll already errors on ErrFieldCount
		if len(rows) < 2 {
			t.Fatalf("%s has %d records, want a header plus at least one data row", name, len(rows))
		}
		for i, row := range rows {
			if len(row) != len(header) {
				t.Errorf("%s record %d has %d fields, want %d (header length)", name, i, len(row), len(header))
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no records were checked -- fixture produced nothing to assert on")
	}
}

// --- AC-3: manifest position and contents -------------------------------------------

func TestBundle_ManifestIsTheLastEntry(t *testing.T) {
	fb := buildFixtureBundle(t)
	r := mustReadZip(t, fb.zipBytes)
	if len(r.File) == 0 {
		t.Fatal("archive has no entries")
	}
	if got := r.File[len(r.File)-1].Name; got != "manifest.json" {
		t.Errorf("last entry = %q, want manifest.json (entries: %v)", got, entryNames(r))
	}
}

func TestBundle_ManifestListsEveryOtherEntry(t *testing.T) {
	fb := buildFixtureBundle(t)
	r := mustReadZip(t, fb.zipBytes)
	doc := readManifest(t, r)

	var wantNames []string
	for _, f := range r.File {
		if f.Name != "manifest.json" {
			wantNames = append(wantNames, f.Name)
		}
	}
	if len(wantNames) == 0 {
		t.Fatal("fixture produced no non-manifest entries -- nothing to assert on")
	}
	if len(doc.Entries) != len(wantNames) {
		t.Fatalf("manifest lists %d entries, want %d (%v)", len(doc.Entries), len(wantNames), wantNames)
	}
	got := map[string]bool{}
	for _, e := range doc.Entries {
		got[e.Name] = true
	}
	for _, name := range wantNames {
		if !got[name] {
			t.Errorf("manifest.Entries missing %q", name)
		}
	}
}

func TestManifest_NotesExplainTheEmptyCellRule(t *testing.T) {
	fb := buildFixtureBundle(t)
	doc := readManifest(t, mustReadZip(t, fb.zipBytes))

	if len(doc.Notes) == 0 {
		t.Fatal("manifest.notes is empty -- nothing to assert on")
	}
	joined := strings.ToLower(strings.Join(doc.Notes, "\n"))
	if !strings.Contains(joined, "null") {
		t.Errorf("manifest.notes %v never states that an empty cell means NULL", doc.Notes)
	}
	for _, col := range []string{"irn", "csid", "qr_payload"} {
		if !strings.Contains(joined, col) {
			t.Errorf("manifest.notes %v never mentions %q (cannot be an empty string, per the DB CHECK)", doc.Notes, col)
		}
	}
}

// TestBundle_SurvivesInterleavedBodyWritesDuringExchangeStage drives the REAL
// bundleWriter through selectExchange's locked call order for >=2 rows, each row's
// two WriteBody calls before its own csv Write -- see
// TestSelectExchange_BodiesStreamPerRowNotAccumulated (exchange_db_test.go:1285-1341)
// for where that order comes from. A single-row fixture would not expose a writer
// that keeps exchange.csv open as a live entry, which a body write would force-close
// mid-write (D-31); this fixture buffers exchange.csv instead, so it must survive.
func TestBundle_SurvivesInterleavedBodyWritesDuringExchangeStage(t *testing.T) {
	var buf bytes.Buffer
	bw := newBundleWriter(&buf)

	e := bw.newCSVEntry("exchange.csv")
	if err := e.Write(exchangeCSVHeader); err != nil {
		t.Fatalf("write exchange.csv header: %v", err)
	}

	type exRow struct{ id, reqBody, respBody string }
	exchanges := []exRow{
		{id: "ex-1", reqBody: "request body one", respBody: "response body one"},
		{id: "ex-2", reqBody: "request body two", respBody: "response body two"},
	}
	rows := make([][]string, 0, len(exchanges))
	for _, ex := range exchanges {
		reqName := "bodies/" + ex.id + ".request"
		respName := "bodies/" + ex.id + ".response"
		if err := bw.WriteBody(reqName, []byte(ex.reqBody)); err != nil {
			t.Fatalf("WriteBody %s: %v", reqName, err)
		}
		if err := bw.WriteBody(respName, []byte(ex.respBody)); err != nil {
			t.Fatalf("WriteBody %s: %v", respName, err)
		}
		row := fixtureRow(exchangeCSVHeader, ex.id)
		row[colIndex(t, exchangeCSVHeader, "exchange_id")] = ex.id
		row[colIndex(t, exchangeCSVHeader, "request_body_file")] = reqName
		row[colIndex(t, exchangeCSVHeader, "response_body_file")] = respName
		if err := e.Write(row); err != nil {
			t.Fatalf("write exchange.csv row for %s: %v", ex.id, err)
		}
		rows = append(rows, row)
	}
	if err := bw.finalizeCSV(e); err != nil {
		t.Fatalf("finalizeCSV exchange.csv: %v", err)
	}
	if err := bw.writeManifest(testManifestParams()); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	if err := bw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := mustReadZip(t, buf.Bytes())

	got := parseCSV(t, readEntry(t, r, "exchange.csv"))
	if len(got) != len(exchanges)+1 {
		t.Fatalf("exchange.csv has %d records, want %d (header + %d rows)", len(got), len(exchanges)+1, len(exchanges))
	}
	for i, want := range rows {
		if !slices.Equal(got[i+1], want) {
			t.Errorf("exchange.csv row %d = %v, want %v", i, got[i+1], want)
		}
	}

	bodyIdx := map[string]int{}
	for _, ex := range exchanges {
		for suffix, want := range map[string]string{".request": ex.reqBody, ".response": ex.respBody} {
			name := "bodies/" + ex.id + suffix
			if data := readEntry(t, r, name); string(data) != want {
				t.Errorf("%s = %q, want %q", name, data, want)
			}
			bodyIdx[name] = entryIndex(t, r, name)
		}
	}

	exIdx := entryIndex(t, r, "exchange.csv")
	for name, idx := range bodyIdx {
		if idx > exIdx {
			t.Errorf("entry %s at index %d, exchange.csv at index %d -- exchange.csv must be finalized after every body it references (D-31)", name, idx, exIdx)
		}
	}

	doc := readManifest(t, r)
	wantEntries := len(bodyIdx) + 1 // bodies + exchange.csv, manifest never lists itself
	if len(doc.Entries) != wantEntries {
		t.Fatalf("manifest lists %d entries, want %d", len(doc.Entries), wantEntries)
	}
	for _, me := range doc.Entries {
		sum := sha256.Sum256(readEntry(t, r, me.Name))
		if got := hex.EncodeToString(sum[:]); got != me.SHA256 {
			t.Errorf("manifest entry %s: sha256 %s, actual %s", me.Name, me.SHA256, got)
		}
	}
}

// --- D-11: unsigned checksum manifest ------------------------------------------------

func TestManifest_NotesDisclaimCryptographicSigning(t *testing.T) {
	fb := buildFixtureBundle(t)
	doc := readManifest(t, mustReadZip(t, fb.zipBytes))

	if len(doc.Notes) == 0 {
		t.Fatal("manifest.notes is empty -- nothing to assert on")
	}
	found := false
	for _, n := range doc.Notes {
		if strings.Contains(strings.ToLower(n), "not a cryptographic signature") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("manifest.notes %v does not disclaim cryptographic signing (D-11)", doc.Notes)
	}
}

// forbiddenKeyName reports whether a JSON key name would suggest cryptographic
// signing (D-11), checked case-insensitively as a substring.
func forbiddenKeyName(key string) bool {
	lower := strings.ToLower(key)
	for _, bad := range []string{"signed", "signature", "hmac"} {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	return false
}

// walkKeys visits every object key in v, at any nesting depth.
func walkKeys(v any, visit func(key string)) {
	switch val := v.(type) {
	case map[string]any:
		for k, sub := range val {
			visit(k)
			walkKeys(sub, visit)
		}
	case []any:
		for _, sub := range val {
			walkKeys(sub, visit)
		}
	}
}

// TestManifest_NoFieldNameImpliesSigning is a separate test from
// TestManifest_NotesDisclaimCryptographicSigning because that note legitimately
// contains the word "signature" -- to negate it. This test scans KEY NAMES only.
func TestManifest_NoFieldNameImpliesSigning(t *testing.T) {
	fb := buildFixtureBundle(t)
	raw := readEntry(t, mustReadZip(t, fb.zipBytes), "manifest.json")

	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal manifest.json generically: %v", err)
	}

	visited := 0
	sawEntries := false
	var bad []string
	walkKeys(generic, func(key string) {
		visited++
		if key == "entries" {
			sawEntries = true
		}
		if forbiddenKeyName(key) {
			bad = append(bad, key)
		}
	})
	if visited == 0 {
		t.Fatal("walked zero keys -- manifest.json parsed as an empty structure")
	}
	if !sawEntries {
		t.Fatal("walk never visited the known \"entries\" key -- walker is not traversing the document")
	}
	if len(bad) > 0 {
		t.Errorf("manifest.json has field name(s) implying cryptographic signing: %v (D-11)", bad)
	}
}

// TestManifest_EntriesNeverMarshalsNull guards the []T-marshals-as-null trap that has
// already shipped bugs elsewhere in this codebase (M4-16). Built through the real
// construction path with zero prior entries recorded, not a hand-built manifestDoc.
func TestManifest_EntriesNeverMarshalsNull(t *testing.T) {
	var buf bytes.Buffer
	bw := newBundleWriter(&buf)

	if err := bw.writeManifest(testManifestParams()); err != nil {
		t.Fatalf("writeManifest with zero prior entries: %v", err)
	}
	if err := bw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := readEntry(t, mustReadZip(t, buf.Bytes()), "manifest.json")
	if len(raw) == 0 {
		t.Fatal("manifest.json is empty -- nothing to assert on")
	}
	if !bytes.Contains(raw, []byte(`"entries"`)) {
		t.Fatalf("manifest.json has no \"entries\" key at all: %s", raw)
	}
	if bytes.Contains(raw, []byte(`"entries":null`)) {
		t.Errorf("manifest.json marshals entries as null with zero recorded entries: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"entries":[]`)) {
		t.Errorf("manifest.json does not marshal a zero-entry list as \"entries\":[]: %s", raw)
	}
}

// --- AC-4: manifest digests are live, not decorative --------------------------------

func TestBundle_ManifestDigestsMatchTheBytes(t *testing.T) {
	fb := buildFixtureBundle(t)
	r := mustReadZip(t, fb.zipBytes)
	doc := readManifest(t, r)

	if len(doc.Entries) == 0 {
		t.Fatal("manifest has no entries -- nothing to assert on")
	}
	for _, me := range doc.Entries {
		data := readEntry(t, r, me.Name)
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != me.SHA256 {
			t.Errorf("entry %s: manifest sha256 %s, actual %s", me.Name, me.SHA256, got)
		}
		if me.Bytes != int64(len(data)) {
			t.Errorf("entry %s: manifest bytes %d, actual %d", me.Name, me.Bytes, len(data))
		}
	}
}

func TestBundle_ManifestDigestFailsOnAMutatedEntry(t *testing.T) {
	fb := buildFixtureBundle(t)
	r := mustReadZip(t, fb.zipBytes)
	doc := readManifest(t, r)

	var want string
	for _, me := range doc.Entries {
		if me.Name == "invoices.csv" {
			want = me.SHA256
		}
	}
	if want == "" {
		t.Fatal("manifest has no invoices.csv entry -- nothing to mutate")
	}

	mutated := bytes.Clone(readEntry(t, r, "invoices.csv"))
	mutated[0] ^= 0xFF // flip one byte; never touch the container itself

	sum := sha256.Sum256(mutated)
	if got := hex.EncodeToString(sum[:]); got == want {
		t.Fatalf("mutated bytes still hash to the recorded digest %s -- fixture did not actually mutate anything", want)
	}
}

// --- AC-5: bodies are verbatim, never duplicated into a CSV cell --------------------

func TestBundle_BodyEntryIsByteIdenticalToSource(t *testing.T) {
	source := []byte("a value with a \"quote\", a\nnewline and a\ttab")

	var buf bytes.Buffer
	bw := newBundleWriter(&buf)
	if err := bw.WriteBody("bodies/x.request", source); err != nil {
		t.Fatalf("WriteBody: %v", err)
	}
	if err := bw.writeManifest(testManifestParams()); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	if err := bw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := readEntry(t, mustReadZip(t, buf.Bytes()), "bodies/x.request")
	if !bytes.Equal(got, source) {
		t.Errorf("body entry = %q, want byte-identical to source %q", got, source)
	}
}

func TestBundle_BodyIsNotAlsoWrittenIntoTheCSVCell(t *testing.T) {
	fb := buildFixtureBundle(t)
	r := mustReadZip(t, fb.zipBytes)

	csvRaw := readEntry(t, r, "exchange.csv")
	if len(csvRaw) == 0 {
		t.Fatal("exchange.csv is empty -- nothing to assert on")
	}
	if !bytes.Contains(csvRaw, []byte("bodies/ex-1.request")) {
		t.Fatalf("exchange.csv does not even carry the body filename -- control needle missing:\n%s", csvRaw)
	}
	if reqBody := fb.bodies["bodies/ex-1.request"]; bytes.Contains(csvRaw, reqBody) {
		t.Errorf("exchange.csv carries the request body bytes verbatim, want only the filename cell:\n%s", csvRaw)
	}
}

// --- AC-6: an abandoned writer is unreadable -----------------------------------------

func TestBundleWriter_AbandonedWithoutCloseIsUnreadable(t *testing.T) {
	var buf bytes.Buffer
	bw := newBundleWriter(&buf)

	for _, spec := range []struct {
		name   string
		header []string
	}{
		{"invoices.csv", invoicesCSVHeader},
		{"status_history.csv", historyCSVHeader},
	} {
		e := bw.newCSVEntry(spec.name)
		if err := e.Write(spec.header); err != nil {
			t.Fatalf("write %s header: %v", spec.name, err)
		}
		if err := e.Write(fixtureRow(spec.header, spec.name)); err != nil {
			t.Fatalf("write %s row: %v", spec.name, err)
		}
		if err := bw.finalizeCSV(e); err != nil {
			t.Fatalf("finalize %s: %v", spec.name, err)
		}
	}
	// Deliberately no bw.Close() -- the abandoned bytes are the thing under test.

	if buf.Len() == 0 {
		t.Fatal("no bytes were written at all -- nothing to assert on")
	}
	_, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err == nil {
		t.Fatal("zip.NewReader accepted an archive abandoned without Close()")
	}
	if !errors.Is(err, zip.ErrFormat) {
		t.Errorf("error = %v, want errors.Is(err, zip.ErrFormat) (measured exact sentinel)", err)
	}
}

// --- AC-7: ZIP64 beyond 65,535 entries ------------------------------------------------

func TestBundleWriter_HandlesMoreThanUint16Entries(t *testing.T) {
	const n = 70000
	var buf bytes.Buffer
	bw := newBundleWriter(&buf)
	for i := 0; i < n; i++ {
		name := "bodies/" + strconv.Itoa(i)
		if err := bw.WriteBody(name, []byte{byte(i)}); err != nil {
			t.Fatalf("WriteBody %s: %v", name, err)
		}
	}
	if err := bw.writeManifest(testManifestParams()); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	if err := bw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := mustReadZip(t, buf.Bytes())
	if len(r.File) != n+1 { // +1 manifest.json
		t.Fatalf("archive has %d entries, want %d (%d bodies + manifest.json)", len(r.File), n+1, n)
	}
}
