// aiimage_internal_test.go: RED acceptance specs for AIR-05-02 (T02-T12) -- the image request,
// the page choice and the format-only reading. Reuses recordingAI/tok/onePage from
// aireading_internal_test.go and Reconcile(Input{}) as T11's oracle.
package extraction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

// aiImgStore is a map-backed PageObject stub for T07: records every key asked, in order, and
// whether each returned body was closed.
type aiImgStore struct {
	bodies map[string][]byte
	failOn string
	keys   []string
	closed []*bool
}

// aiImgBody is a ReadCloser that flags itself closed.
type aiImgBody struct {
	*bytes.Reader
	closed *bool
}

func (b *aiImgBody) Close() error {
	*b.closed = true
	return nil
}

func (s *aiImgStore) get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	s.keys = append(s.keys, key)
	if key == s.failOn {
		return nil, 0, errors.New("aiimage test: object store failed on " + key)
	}
	body := s.bodies[key]
	closed := false
	s.closed = append(s.closed, &closed)
	return &aiImgBody{Reader: bytes.NewReader(body), closed: &closed}, int64(len(body)), nil
}

func (s *aiImgStore) allClosed() bool {
	if len(s.closed) == 0 {
		return false
	}
	for _, c := range s.closed {
		if !*c {
			return false
		}
	}
	return true
}

// T02
func TestAskAIPages_SendsOneImageCall(t *testing.T) {
	a := []byte{0x01, 0x02, 0x03}
	b := []byte{0x04, 0x05}
	stub := &recordingAI{enabled: true, answer: map[string]any{}}

	askAIPages(context.Background(), stub, [][]byte{a, b}, "H")

	if len(stub.calls) != 1 {
		t.Fatalf("calls = %d, want exactly 1", len(stub.calls))
	}
	req := stub.calls[0]
	if req.Purpose != ai.PurposeDocument {
		t.Errorf("Purpose = %q, want %q", req.Purpose, ai.PurposeDocument)
	}
	if req.System != aiSystem {
		t.Errorf("System = %q, want aiSystem", req.System)
	}
	if req.Text != aiImageIntro {
		t.Errorf("Text = %q, want aiImageIntro", req.Text)
	}
	if len(req.Pages) != 2 || !bytes.Equal(req.Pages[0], a) || !bytes.Equal(req.Pages[1], b) {
		t.Errorf("Pages = %v, want [a, b] in order", req.Pages)
	}
	if req.FakeHint != "H" {
		t.Errorf("FakeHint = %q, want %q", req.FakeHint, "H")
	}
	if req.SchemaName != "invoice_fields" {
		t.Errorf("SchemaName = %q, want invoice_fields", req.SchemaName)
	}
	if string(req.Schema) != string(aiFieldSchema) {
		t.Errorf("Schema = %s, want aiFieldSchema %s", req.Schema, aiFieldSchema)
	}
}

// T03
func TestAskAIPages_OffOrNilMakesNoCall(t *testing.T) {
	pngs := [][]byte{{0x01}}

	if got, failed := askAIPages(context.Background(), nil, pngs, "H"); got != nil || failed {
		t.Errorf("askAIPages(nil) = %v, %v; want nil, false", got, failed)
	}

	off := &recordingAI{enabled: false}
	got, failed := askAIPages(context.Background(), off, pngs, "H")
	if got != nil || failed {
		t.Errorf("askAIPages(off) = %v, %v; want nil, false", got, failed)
	}
	if len(off.calls) != 0 {
		t.Errorf("askAIPages(off) calls = %d, want 0", len(off.calls))
	}

	on := &recordingAI{enabled: true, answer: map[string]any{"total": "1935.00"}}
	if got, _ := askAIPages(context.Background(), on, pngs, "H"); len(on.calls) != 1 || got["total"] != "1935.00" {
		t.Errorf("control: askAIPages(on) calls = %d, answer = %v; want 1 call and total 1935.00", len(on.calls), got)
	}
}

// T04
func TestAskAIPages_SharesAskAIsFailurePolicy(t *testing.T) {
	pages := onePage(1, tok("Invoice", 1, 0.10, 0.10, 0.20, 0.12))
	pngs := [][]byte{{0x01}}
	answer := map[string]any{"total": "1935.00"}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable", ai.ErrUnavailable, true},
		{"fake unavailable", fmt.Errorf("%w (fake)", ai.ErrUnavailable), true},
		{"refused", errors.New("ai: refused: HTTP 402"), true},
		{"invalid request", errors.New("ai: invalid request: x"), true},
		{"fake answer", errors.New("ai: fake answer: x"), true},
		{"canceled", context.Canceled, false},
		{"wrapped deadline", fmt.Errorf("ai: %w", context.DeadlineExceeded), false},
		{"off", ai.ErrOff, false},
	}
	for _, c := range cases {
		stubImg := &recordingAI{enabled: true, answer: answer, err: c.err}
		gotImg, failedImg := askAIPages(context.Background(), stubImg, pngs, "H")
		if gotImg != nil {
			t.Errorf("%s: askAIPages answer = %v, want nil", c.name, gotImg)
		}
		if failedImg != c.want {
			t.Errorf("%s: askAIPages flag = %v, want %v", c.name, failedImg, c.want)
		}

		stubText := &recordingAI{enabled: true, answer: answer, err: c.err}
		gotText, failedText := askAI(context.Background(), stubText, pages)
		if failedImg != failedText || !reflect.DeepEqual(gotImg, gotText) {
			t.Errorf("%s: askAIPages = %v,%v; askAI = %v,%v -- want the same policy", c.name, gotImg, failedImg, gotText, failedText)
		}
	}
}

// T05
func TestAskAIPages_KeepsOnlyNonBlankHeaderStrings(t *testing.T) {
	stub := &recordingAI{enabled: true, answer: map[string]any{
		"invoice_number": "  ",
		"total":          json.Number("12"), // not a string: dropped, same as askAI
		"currency":       "NGN",
		"line_items":     "x",
	}}
	got, _ := askAIPages(context.Background(), stub, [][]byte{{0x01}}, "H")
	want := map[string]string{"currency": "NGN"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("askAIPages(mixed answer) = %v, want %v", got, want)
	}
}

// T06
func TestAIImagePages_FirstAndLastPage(t *testing.T) {
	p := func(n int) PageImage { return PageImage{Page: n, StorageKey: fmt.Sprintf("p%04d", n)} }

	if got := aiImagePages(nil); len(got) != 0 {
		t.Errorf("aiImagePages(0 images) = %v, want empty", got)
	}

	one := []PageImage{p(1)}
	if got := aiImagePages(one); len(got) != 1 || got[0].StorageKey != "p0001" {
		t.Errorf("aiImagePages(1 image) = %v, want [p1]", got)
	}

	two := []PageImage{p(1), p(2)}
	if got := aiImagePages(two); len(got) != 2 || got[0].StorageKey != "p0001" || got[1].StorageKey != "p0002" {
		t.Errorf("aiImagePages(2 images) = %v, want [p1, p2]", got)
	}

	five := []PageImage{p(1), p(2), p(3), p(4), p(5)}
	fiveCopy := append([]PageImage(nil), five...)
	if got := aiImagePages(five); len(got) != 2 || got[0].StorageKey != "p0001" || got[1].StorageKey != "p0005" {
		t.Errorf("aiImagePages(5 images) = %v, want [p1, p5]", got)
	}
	if !reflect.DeepEqual(five, fiveCopy) {
		t.Errorf("aiImagePages mutated its input slice: got %v, want %v", five, fiveCopy)
	}
}

// T07
func TestReadPagePNGs_ReadsEachKeyAndClosesEveryBody(t *testing.T) {
	a := []byte{0xAA}
	b := []byte{0xBB}
	pages := []PageImage{{Page: 1, StorageKey: "k1"}, {Page: 2, StorageKey: "k2"}}

	store := &aiImgStore{bodies: map[string][]byte{"k1": a, "k2": b}}
	got, err := readPagePNGs(context.Background(), store.get, pages)
	if err != nil {
		t.Fatalf("readPagePNGs = %v, want nil error", err)
	}
	if len(got) != 2 || !bytes.Equal(got[0], a) || !bytes.Equal(got[1], b) {
		t.Errorf("readPagePNGs = %v, want [A, B] in order", got)
	}
	if want := []string{"k1", "k2"}; !reflect.DeepEqual(store.keys, want) {
		t.Errorf("keys asked = %v, want %v", store.keys, want)
	}
	if !store.allClosed() {
		t.Errorf("not every body was closed: %d bodies read, allClosed = %v", len(store.closed), store.allClosed())
	}

	failStore := &aiImgStore{bodies: map[string][]byte{"k1": a, "k2": b}, failOn: "k2"}
	if _, err := readPagePNGs(context.Background(), failStore.get, pages); err == nil {
		t.Fatal("readPagePNGs(failing on k2) = nil error, want an error")
	}
	if len(failStore.closed) != 1 || !*failStore.closed[0] {
		t.Errorf("k1's body was not read and closed before k2 failed")
	}
}

// T08
func TestImageReading_APassingValueIsDecidedWithNoRegion(t *testing.T) {
	answer := map[string]string{
		"invoice_number": "INV-5520",
		"total":          "1,935.00",
		"issue_date":     "2026-07-14",
	}
	want := map[string]string{"invoice_number": "INV-5520", "total": "1935.00", "issue_date": "2026-07-14"}

	got := imageReading(answer)
	if len(got) == 0 {
		t.Fatal("imageReading returned no rows")
	}

	seen := 0
	for _, row := range got {
		if row.Name == "line_items" {
			if row.Reason != ReasonMissing {
				t.Errorf("line_items reason = %q, want missing", row.Reason)
			}
			continue
		}
		wantVal, decided := want[row.Name]
		if !decided {
			if row.Reason != ReasonMissing {
				t.Errorf("%s reason = %q, want missing (not answered)", row.Name, row.Reason)
			}
			continue
		}
		seen++
		if row.Value == nil || *row.Value != wantVal {
			t.Errorf("%s value = %v, want %q", row.Name, row.Value, wantVal)
		}
		if row.Region != nil {
			t.Errorf("%s region = %v, want nil", row.Name, row.Region)
		}
		if row.Reason != ReasonNone {
			t.Errorf("%s reason = %q, want none", row.Name, row.Reason)
		}
		if len(row.Alternatives) != 0 {
			t.Errorf("%s alternatives = %v, want empty", row.Name, row.Alternatives)
		}
	}
	if seen != len(want) {
		t.Errorf("decided %d of %d expected fields", seen, len(want))
	}
}

// T09
func TestImageReading_AFailingValueIsDoubtfulWithTheAIText(t *testing.T) {
	cases := []struct {
		field, raw, trimmed string
	}{
		{"buyer_tin", " 9999999-1202 ", "9999999-1202"}, // dropped digit: normalizeTIN rejects
		{"issue_date", "03/04/2026", "03/04/2026"},      // two readings: day-first/month-first
		{"invoice_number", "20417", "20417"},            // all digits, amount-shaped, no label possible
	}
	for _, c := range cases {
		got := imageReading(map[string]string{c.field: c.raw})
		if len(got) == 0 {
			t.Fatalf("%s: imageReading returned no rows", c.field)
		}
		var row *FieldResult
		for i := range got {
			if got[i].Name == c.field {
				row = &got[i]
				break
			}
		}
		if row == nil {
			t.Fatalf("%s: no row named %q in result", c.field, c.field)
		}
		if row.Value != nil {
			t.Errorf("%s: value = %v, want nil", c.field, *row.Value)
		}
		if row.Region != nil {
			t.Errorf("%s: region = %v, want nil", c.field, row.Region)
		}
		if row.Reason != ReasonUnreadable {
			t.Errorf("%s: reason = %q, want unreadable", c.field, row.Reason)
		}
		trimmed := c.trimmed
		want := []Field{{Name: c.field, Value: &trimmed, Region: nil, Reason: ReasonNone}}
		if !reflect.DeepEqual(row.Alternatives, want) {
			t.Errorf("%s: alternatives = %+v, want %+v", c.field, row.Alternatives, want)
		}
	}
}

// T10
func TestImageReading_RunsNoTextCheck(t *testing.T) {
	answer := map[string]string{
		"buyer_name":    "ZENITH HOLDINGS LIMITED",
		"supplier_name": "Account Name Holdings", // payment-label-shaped text; checkAI's check (c) would fail it
	}
	got := imageReading(answer)
	if len(got) == 0 {
		t.Fatal("imageReading returned no rows")
	}
	for name, want := range answer {
		var row *FieldResult
		for i := range got {
			if got[i].Name == name {
				row = &got[i]
				break
			}
		}
		if row == nil {
			t.Fatalf("no row named %q", name)
		}
		if row.Value == nil || *row.Value != want {
			t.Errorf("%s value = %v, want %q -- a value the text checks would refuse must still be decided (no text check runs)", name, row.Value, want)
		}
		if row.Reason != ReasonNone {
			t.Errorf("%s reason = %q, want none", name, row.Reason)
		}
	}
}

// T11
func TestImageReading_KeepsReconcilesRowSet(t *testing.T) {
	base := Reconcile(Input{})
	if len(base) == 0 {
		t.Fatal("Reconcile(Input{}) returned no rows -- nothing to compare against")
	}

	answer := map[string]string{
		"invoice_number": "INV-5520",
		"total":          "1935.00",
		"nonsense_key":   "x", // not a header field: must change nothing
	}
	got := imageReading(answer)
	if len(got) != len(base) {
		t.Fatalf("imageReading returned %d rows, want %d (Reconcile(Input{})'s shape)", len(got), len(base))
	}
	for i := range base {
		if got[i].Name != base[i].Name {
			t.Errorf("row %d name = %q, want %q (Reconcile(Input{})'s order)", i, got[i].Name, base[i].Name)
		}
	}
	last := got[len(got)-1]
	if last.Name != "line_items" || last.Reason != ReasonMissing {
		t.Errorf("last row = %+v, want line_items missing", last)
	}
}

// T12
func TestAIUnavailableResults_IsTheAIR04Marker(t *testing.T) {
	got := aiUnavailableResults()
	if len(got) != 1 {
		t.Fatalf("aiUnavailableResults = %d rows, want exactly 1", len(got))
	}
	row := got[0]
	if row.Name != aiUnavailableField {
		t.Errorf("Name = %q, want %q", row.Name, aiUnavailableField)
	}
	if row.Value != nil {
		t.Errorf("Value = %v, want nil", row.Value)
	}
	if row.Region != nil {
		t.Errorf("Region = %v, want nil", row.Region)
	}
	if row.Reason != ReasonUnreadable {
		t.Errorf("Reason = %q, want unreadable", row.Reason)
	}
	if len(row.Alternatives) != 0 {
		t.Errorf("Alternatives = %v, want empty", row.Alternatives)
	}
}
