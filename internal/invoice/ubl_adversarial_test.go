// Adversarial coverage for GET /v1/invoices/{id}/ubl, added at QA over the
// AC-derived rows in ubl_test.go: the store-rejected 401, filenames the AC
// rows never exercise (space / slash / non-ASCII / CRLF), route resolution and
// HTTP methods on a real ServeMux, every gap count 1..6, and byte fidelity
// over the wire rather than into a recorder.
package invoice

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/internal/ubl"
)

// The pattern cmd/invoice/main.go registers. Pinned here so the ServeMux rows
// below resolve the real string, not a paraphrase of it.
const ublRoutePattern = "GET /v1/invoices/{id}/ubl"

// --- the 401 the store decides (status table row 2, no AC-derived row) -------

// db.ErrNoTenant is a DIFFERENT 401 from the missing-identity one: the identity
// is present, the store rejects the tenant. It reaches the wire through
// statusForErr, which the identity-first guard never touches.
func TestUBLHandler_StoreErrNoTenantIs401(t *testing.T) {
	id := ublTestIdentity()
	rec := doUBL(t, ublGetErr(db.ErrNoTenant), &id, uuid.NewString())

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"unauthorized"}` {
		t.Errorf("body = %q, want %q", got, `{"error":"unauthorized"}`)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition = %q, want it absent on a refusal", got)
	}
}

// A 500 store error is the reachable half of "never a partial XML body": the
// envelope, the JSON headers, no XML, and the log line GetHandler's sibling
// arm emits.
func TestUBLHandler_UnexpectedStoreErrorIs500AndIsLogged(t *testing.T) {
	inv := completeUBLInvoice(t, "INV-0500")
	get := func(ctx context.Context, id string) (Invoice, error) {
		return inv, errors.New("pool exhausted")
	}

	var buf bytes.Buffer
	r := httptest.NewRequest("GET", "/v1/invoices/"+inv.ID+"/ubl", nil)
	r.SetPathValue("id", inv.ID)
	r = r.WithContext(auth.WithIdentity(r.Context(), ublTestIdentity()))
	rec := httptest.NewRecorder()
	UBLHandler(get, slog.New(slog.NewJSONHandler(&buf, nil))).ServeHTTP(rec, r)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"internal server error"}` {
		t.Errorf("body = %q, want %q", got, `{"error":"internal server error"}`)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition = %q, want it absent on a 500", got)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("<?xml")) {
		t.Errorf("body = %s, must contain no XML", rec.Body.Bytes())
	}
	if buf.Len() == 0 {
		t.Error("nothing was logged, want the 500 arm to emit via the injected *slog.Logger")
	}
	if !strings.Contains(buf.String(), "pool exhausted") {
		t.Errorf("log = %s, want the underlying error in it", buf.String())
	}
}

// Every stub closure below ignores its arguments, so nothing else here would
// notice the handler passing a constant instead of the path value. Only the
// db-backed row would, and only when the DSNs are set.
func TestUBLHandler_PassesThePathIDAndRequestContextToTheStore(t *testing.T) {
	inv := completeUBLInvoice(t, "INV-0001")
	want := uuid.NewString()
	id := ublTestIdentity()

	var gotID string
	var gotTenant string
	get := func(ctx context.Context, gotArg string) (Invoice, error) {
		gotID = gotArg
		if ident, ok := auth.IdentityFromContext(ctx); ok {
			gotTenant = ident.TenantID
		}
		return inv, nil
	}

	rec := doUBL(t, get, &id, want)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if gotID != want {
		t.Errorf("store received id %q, want the {id} path value %q", gotID, want)
	}
	if gotTenant != id.TenantID {
		t.Errorf("store saw tenant %q, want the request context's %q", gotTenant, id.TenantID)
	}
}

// --- filenames the AC rows never exercise ------------------------------------

// A space or a slash forces the quoted branch; a non-ASCII rune forces RFC 2231
// (the filename*= form), an entirely different arm of mime.FormatMediaType.
//
// wantExtended is load-bearing on the last row: an always-quote implementation
// emits raw UTF-8 in a quoted-string, which mime.ParseMediaType still decodes
// to the right value -- so asserting the value alone passes on the bug.
func TestUBLHandler_FilenameSurvivesSpacesSlashesAndNonASCII(t *testing.T) {
	cases := []struct {
		name         string
		number       string
		wantFilename string
		wantExtended bool
	}{
		{"space", "INV 001", "INV 001.xml", false},
		{"slash", "INV/2026/001", "INV/2026/001.xml", false},
		{"semicolon", "INV;001", "INV;001.xml", false},
		{"non-ascii", "Acme₦-01", "Acme₦-01.xml", true},
		{"non-latin", "τιμολόγιο-1", "τιμολόγιο-1.xml", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv := completeUBLInvoice(t, tc.number)
			id := ublTestIdentity()
			rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			cd := rec.Header().Get("Content-Disposition")
			mediatype, params, err := mime.ParseMediaType(cd)
			if err != nil {
				t.Fatalf("ParseMediaType(%q) = %v, want err == nil", cd, err)
			}
			if mediatype != "attachment" {
				t.Errorf("Content-Disposition type = %q, want %q", mediatype, "attachment")
			}
			if got := params["filename"]; got != tc.wantFilename {
				t.Errorf("filename = %q, want %q", got, tc.wantFilename)
			}
			gotExtended := strings.Contains(cd, "filename*=utf-8''")
			if gotExtended != tc.wantExtended {
				t.Errorf("Content-Disposition = %q: RFC 2231 filename*= present = %v, want %v",
					cd, gotExtended, tc.wantExtended)
			}
			if tc.wantExtended && strings.Contains(cd, tc.number) {
				t.Errorf("Content-Disposition = %q carries the raw non-ASCII number; RFC 8187 requires it percent-encoded", cd)
			}
		})
	}
}

// An invoice number is free text, so a CR/LF in it must not reach the header
// value. mime.FormatMediaType diverts control bytes into the percent-encoded
// RFC 2231 branch; naive quoting would splice them in raw.
func TestUBLHandler_FilenameCannotSplitTheResponseHeader(t *testing.T) {
	inv := completeUBLInvoice(t, "INV\r\nX-Injected: yes")
	id := ublTestIdentity()
	rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	if strings.ContainsAny(cd, "\r\n") {
		t.Errorf("Content-Disposition = %q carries a raw CR/LF", cd)
	}
	if got := rec.Header().Get("X-Injected"); got != "" {
		t.Errorf("X-Injected = %q, want no injected header", got)
	}
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		t.Fatalf("ParseMediaType(%q) = %v, want err == nil", cd, err)
	}
	if got := params["filename"]; got != "INV\r\nX-Injected: yes.xml" {
		t.Errorf("filename = %q, want the number round-tripped verbatim", got)
	}
}

// ubl.Missing gates on TrimSpace, so a blank or whitespace-only number is a 409
// and the empty-filename case (mime renders `filename=.xml`) is unreachable.
func TestUBLHandler_BlankInvoiceNumberIs409AndNeverShipsAFilename(t *testing.T) {
	for _, number := range []string{"", "   ", "\t\n"} {
		t.Run(fmt.Sprintf("%q", number), func(t *testing.T) {
			inv := completeUBLInvoice(t, "INV-0001")
			inv.InvoiceNumber = number
			id := ublTestIdentity()
			rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Disposition"); got != "" {
				t.Errorf("Content-Disposition = %q, want no filename header at all", got)
			}
			if got := ublErrorValue(t, rec); !strings.Contains(got, "an invoice number") {
				t.Errorf("reason = %q, want it to name the missing invoice number", got)
			}
		})
	}
}

// Padding passes ubl.Missing's TrimSpace gate, and the server deliberately does
// NOT sanitise -- FormatMediaType quotes it instead. Pinned so a future reader
// sees this as the decision it is, not drift
// ([download-filename-sanitised-client-side] is the client-side half).
func TestUBLHandler_PaddedInvoiceNumberKeepsItsPadding(t *testing.T) {
	inv := completeUBLInvoice(t, " INV-1 ")
	id := ublTestIdentity()
	rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		t.Fatalf("ParseMediaType(%q) = %v, want err == nil", cd, err)
	}
	if got := params["filename"]; got != " INV-1 .xml" {
		t.Errorf("filename = %q, want %q (unsanitised, quoted)", got, " INV-1 .xml")
	}
}

// --- every gap count, not just the 1/2/3/6 boundaries ------------------------

// 4 and 5 gaps are the counts no AC row reaches, and an off-by-one in the
// join's slice bound shows up there first.
func TestUBLBlockedReason_EveryGapCountFromOneToSix(t *testing.T) {
	const prefix = "This invoice cannot be rendered as a UBL document — it is missing "
	all := ubl.Missing(submission.Canonical{})
	if len(all) != 6 {
		t.Fatalf("ubl.Missing(zero Canonical) = %v, want the six labels this table slices", all)
	}
	want := []string{
		prefix + "an invoice number.",
		prefix + "an invoice number and an issue date.",
		prefix + "an invoice number, an issue date and a currency.",
		prefix + "an invoice number, an issue date, a currency and a supplier name.",
		prefix + "an invoice number, an issue date, a currency, a supplier name and a buyer name.",
		prefix + "an invoice number, an issue date, a currency, a supplier name, a buyer name and at least one line item.",
	}
	for n := 1; n <= 6; n++ {
		got := ublBlockedReason(all[:n])
		if got == nil {
			t.Fatalf("ublBlockedReason(%d gaps) = nil, want a reason string", n)
		}
		if *got != want[n-1] {
			t.Errorf("%d gaps:\n got  %q\n want %q", n, *got, want[n-1])
		}
		if n > 1 && strings.Count(*got, " and ") != 1 {
			t.Errorf("%d gaps: reason = %q, want exactly one \" and \"", n, *got)
		}
		if wantCommas := max(n-2, 0); strings.Count(*got, ",") != wantCommas {
			t.Errorf("%d gaps: reason = %q has %d commas, want %d",
				n, *got, strings.Count(*got, ","), wantCommas)
		}
	}
}

// The same six counts through the handler, from real invoices: the 409 body
// must stay decodable JSON and carry ublBlockedReason's own string.
func TestUBLHandler_409BodyIsValidJSONForEveryGapCount(t *testing.T) {
	const prefix = "This invoice cannot be rendered as a UBL document — it is missing "
	// Each row clears one more field than the row above it, in reverse
	// ubl.Missing order, so the label list grows from the tail.
	cases := []struct {
		gaps int
		mut  func(*Invoice)
		want string
	}{
		{1, func(i *Invoice) { i.LineItems = nil }, prefix + "at least one line item."},
		{2, func(i *Invoice) { i.BuyerName = nil }, prefix + "a buyer name and at least one line item."},
		{3, func(i *Invoice) { i.SupplierName = nil }, prefix + "a supplier name, a buyer name and at least one line item."},
		{4, func(i *Invoice) { i.Currency = nil }, prefix + "a currency, a supplier name, a buyer name and at least one line item."},
		{5, func(i *Invoice) { i.IssueDate = nil }, prefix + "an issue date, a currency, a supplier name, a buyer name and at least one line item."},
		{6, func(i *Invoice) { i.InvoiceNumber = "" }, prefix + "an invoice number, an issue date, a currency, a supplier name, a buyer name and at least one line item."},
	}

	inv := completeUBLInvoice(t, "INV-0409")
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d-gaps", tc.gaps), func(t *testing.T) {
			tc.mut(&inv)
			if got := ubl.Missing(SubmissionCanonical(inv)); len(got) != tc.gaps {
				t.Fatalf("fixture has %d gaps (%v), want %d", len(got), got, tc.gaps)
			}
			id := ublTestIdentity()
			rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
			}
			// ublErrorValue fails the test unless the body decodes as JSON
			// with exactly the one "error" key.
			if got := ublErrorValue(t, rec); got != tc.want {
				t.Errorf("reason =\n %q\nwant\n %q", got, tc.want)
			}
		})
	}
}

// --- route resolution and HTTP methods ---------------------------------------

// The registration itself has no other test: every row above drives the handler
// factory directly, so a typo'd pattern or a swapped store method in main.go
// would be invisible.
func TestUBLRoute_IsRegisteredInTheInvoiceServiceMain(t *testing.T) {
	src, err := os.ReadFile("../../cmd/invoice/main.go")
	if err != nil {
		t.Fatalf("read cmd/invoice/main.go: %v", err)
	}
	const want = `app.Mux.HandleFunc("` + ublRoutePattern + `", invoice.UBLHandler(store.Get, app.Logger))`
	if !bytes.Contains(src, []byte(want)) {
		t.Errorf("cmd/invoice/main.go does not register\n\t%s", want)
	}
}

// ublSiblingMux registers the /v1/invoices patterns cmd/invoice/main.go serves
// with sentinel handlers that name themselves, so a request's resolution is
// observable.
func ublSiblingMux(reverse bool) *http.ServeMux {
	patterns := []string{
		"GET /v1/invoices",
		"GET /v1/invoices/violation-summary",
		"GET /v1/invoices/{id}",
		"GET /v1/invoices/{id}/history",
		"GET /v1/invoices/{id}/source-document",
		ublRoutePattern,
	}
	if reverse {
		for i, j := 0, len(patterns)-1; i < j; i, j = i+1, j-1 {
			patterns[i], patterns[j] = patterns[j], patterns[i]
		}
	}
	mux := http.NewServeMux()
	for _, p := range patterns {
		name := p
		mux.HandleFunc(name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Matched-Pattern", name)
			w.Header().Set("X-Matched-ID", r.PathValue("id"))
			w.WriteHeader(http.StatusOK)
		})
	}
	return mux
}

// Go 1.22+ ServeMux resolves by specificity, so /ubl can neither swallow nor be
// swallowed by GET /v1/invoices/{id} or the literal violation-summary route --
// asserted, not reasoned about, and in both registration orders.
func TestUBLRoute_DoesNotShadowOrGetShadowed(t *testing.T) {
	invID := uuid.NewString()
	cases := []struct {
		path        string
		wantPattern string
		wantID      string
	}{
		{"/v1/invoices", "GET /v1/invoices", ""},
		{"/v1/invoices/violation-summary", "GET /v1/invoices/violation-summary", ""},
		{"/v1/invoices/" + invID, "GET /v1/invoices/{id}", invID},
		{"/v1/invoices/" + invID + "/history", "GET /v1/invoices/{id}/history", invID},
		{"/v1/invoices/" + invID + "/source-document", "GET /v1/invoices/{id}/source-document", invID},
		{"/v1/invoices/" + invID + "/ubl", ublRoutePattern, invID},
		// A literal third segment does not shadow the wildcard one level down:
		// this reaches the UBL handler and 400s on the malformed id.
		{"/v1/invoices/violation-summary/ubl", ublRoutePattern, "violation-summary"},
		// {id} spans exactly one segment, so nothing matches five deep.
		{"/v1/invoices/" + invID + "/ubl/extra", "", ""},
	}

	for _, reverse := range []bool{false, true} {
		mux := ublSiblingMux(reverse)
		for _, tc := range cases {
			t.Run(fmt.Sprintf("reverse=%v %s", reverse, tc.path), func(t *testing.T) {
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))

				if tc.wantPattern == "" {
					if rec.Code != http.StatusNotFound {
						t.Fatalf("status = %d (matched %q), want 404",
							rec.Code, rec.Header().Get("X-Matched-Pattern"))
					}
					return
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (nothing matched)", rec.Code)
				}
				if got := rec.Header().Get("X-Matched-Pattern"); got != tc.wantPattern {
					t.Errorf("matched %q, want %q", got, tc.wantPattern)
				}
				if got := rec.Header().Get("X-Matched-ID"); got != tc.wantID {
					t.Errorf("{id} = %q, want %q", got, tc.wantID)
				}
			})
		}
	}
}

// ublWireServer serves inv on the real route through a real ServeMux and a real
// net/http server -- the only place HEAD's body suppression and chunked writes
// are observable (a ResponseRecorder shows neither).
func ublWireServer(t *testing.T, inv Invoice) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(ublRoutePattern, ublAuthed(UBLHandler(ublGetOK(inv), nil)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// ublAuthed stands in for the gateway's identity injection.
func ublAuthed(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), ublTestIdentity())))
	})
}

// Registering GET also serves HEAD (net/http since 1.22): the handler runs and
// the server drops the body. Everything else on the path is a 405 the mux
// answers, never the handler. Both are acceptable for a read-only route --
// pinned so a future method-sensitive change is visible.
func TestUBLRoute_HEADIsServedAndOtherMethodsAre405(t *testing.T) {
	inv := completeUBLInvoice(t, "INV-0001")
	srv := ublWireServer(t, inv)

	t.Run("HEAD", func(t *testing.T) {
		req, _ := http.NewRequest("HEAD", srv.URL+"/v1/invoices/"+inv.ID+"/ubl", nil)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("HEAD: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if len(body) != 0 {
			t.Errorf("HEAD body = %q, want empty", body)
		}
		if got := resp.Header.Get("Content-Type"); got != "application/xml; charset=utf-8" {
			t.Errorf("Content-Type = %q, want the same header GET sends", got)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
		}
	})

	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			req, _ := http.NewRequest(method, srv.URL+"/v1/invoices/"+inv.ID+"/ubl", nil)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)

			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", resp.StatusCode)
			}
			if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "GET") || !strings.Contains(allow, "HEAD") {
				t.Errorf("Allow = %q, want it to list GET and HEAD", allow)
			}
			if bytes.Contains(body, []byte("<?xml")) {
				t.Errorf("body = %s, must contain no XML", body)
			}
		})
	}
}

// A recorder buffers, so it cannot see a truncating or double-buffering write.
// 4000 lines puts the document well past net/http's ~2KB auto-Content-Length
// threshold, i.e. onto the chunked path.
func TestUBLHandler_LargeInvoiceIsServedWholeOverTheWire(t *testing.T) {
	inv := completeUBLInvoice(t, "INV-BIG")
	inv.LineItems = nil
	for i := 1; i <= 4000; i++ {
		inv.LineItems = append(inv.LineItems, LineItem{
			ID:          uuid.NewString(),
			LineNo:      i,
			Description: ublStr(fmt.Sprintf("Widget %d — ασφάλεια & <escaping>", i)),
			Quantity:    ublStr("2"),
			UnitPrice:   ublStr("50.00"),
			LineTotal:   ublStr("100.00"),
			LineTax:     ublStr("7.50"),
		})
	}
	want, err := ubl.Render(SubmissionCanonical(inv))
	if err != nil {
		t.Fatalf("ubl.Render (fixture): %v", err)
	}
	if len(want) < 1<<20 {
		t.Fatalf("fixture render is %d bytes, want > 1 MiB so the chunked path is exercised", len(want))
	}

	srv := ublWireServer(t, inv)
	resp, err := srv.Client().Get(srv.URL + "/v1/invoices/" + inv.ID + "/ubl")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(got) != len(want) {
		t.Fatalf("body is %d bytes, want %d -- truncated or padded on the wire", len(got), len(want))
	}
	if !bytes.Equal(got, want) {
		t.Error("body differs from ubl.Render's bytes over the wire")
	}
	if !bytes.HasSuffix(got, []byte("</Invoice>")) {
		t.Errorf("body ends %q, want a closed root element", got[max(len(got)-40, 0):])
	}
	if err := xml.Unmarshal(got, new(struct{ XMLName xml.Name })); err != nil {
		t.Errorf("served body does not parse as XML: %v", err)
	}
}

// --- db-backed: the real store's own bytes -----------------------------------

// The stubbed rows above cannot see a pgx scanning defect (numeric -> *string,
// timestamptz -> *time.Time): only a real row can. This is also the positive
// control the cross-tenant row lacks -- without it, a route that 404s for
// everyone would satisfy that test.
func TestRLS_UBLHandlerServesTheOwningTenantsInvoiceFromTheRealStore(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenant := seedTenant(t, super, "BUG-04-02 UBL owning tenant")
	entity := seedEntity(t, super, tenant, "BUG-04-02 UBL owning entity")

	store := NewStore(app)
	issued := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	identity := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenant}
	c := auth.WithIdentity(ctx, identity)
	created, err := store.Create(c, CreateInput{
		EntityID:      entity,
		InvoiceNumber: "BUG-04-02 OWN/01",
		IssueDate:     &issued,
		BuyerName:     ublStr("Beta Buyers Ltd"),
		Currency:      ublStr("NGN"),
		Subtotal:      ublStr("100.00"),
		VAT:           ublStr("7.50"),
		Total:         ublStr("107.50"),
		LineItems: []LineItemInput{{
			Description: ublStr("Widget"),
			Quantity:    ublStr("2"),
			UnitPrice:   ublStr("50.00"),
			LineTotal:   ublStr("100.00"),
			LineTax:     ublStr("7.50"),
		}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	hydrated, err := store.Get(c, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want, err := ubl.Render(SubmissionCanonical(hydrated))
	if err != nil {
		t.Fatalf("ubl.Render (db row): %v", err)
	}

	rec := doUBL(t, store.Get, &identity, created.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("body = %q, want the db row's rendered bytes (%q)", rec.Body.Bytes(), want)
	}
	// The db row's own values must survive into the document, not defaults.
	for _, frag := range []string{"<cbc:ID>BUG-04-02 OWN/01</cbc:ID>", "2026-03-14", "NGN", "Beta Buyers Ltd"} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(frag)) {
			t.Errorf("served document is missing %q", frag)
		}
	}
	// The number has a space and a slash, so it takes the quoted branch.
	cd := rec.Header().Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		t.Fatalf("ParseMediaType(%q) = %v, want err == nil", cd, err)
	}
	if got := params["filename"]; got != "BUG-04-02 OWN/01.xml" {
		t.Errorf("filename = %q, want %q", got, "BUG-04-02 OWN/01.xml")
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), new(struct{ XMLName xml.Name })); err != nil {
		t.Errorf("served body does not parse as XML: %v", err)
	}
}
