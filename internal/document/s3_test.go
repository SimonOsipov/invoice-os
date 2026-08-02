// s3_test.go: AC-2/4/5's specs, authored before internal/document/{objectstore,s3}.go exist.
//
// The wire tests drive the real aws-sdk-go-v2 client through an injected
// http.RoundTripper. That is a deliberate deviation from the house
// httptest.NewServer idiom (internal/invoice/validator_test.go:100): httptest
// hands out an IP-host endpoint, and the SDK falls back to PATH style against
// an IP — so a correct [no-path-style] implementation would look broken and
// invite a UsePathStyle:true "fix" that inverts the decision.
package document_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

// --- ConfigFromEnv (AC-2) -----------------------------------------------

const (
	envBucket    = "DOCUMENT_BUCKET"
	envEndpoint  = "DOCUMENT_ENDPOINT"
	envRegion    = "DOCUMENT_REGION"
	envAccessKey = "DOCUMENT_ACCESS_KEY_ID"
	envSecretKey = "DOCUMENT_SECRET_ACCESS_KEY"
)

// Distinct per variable so a crossed field assignment (Region read from
// DOCUMENT_ENDPOINT) fails instead of coincidentally matching.
var documentEnvValues = map[string]string{
	envBucket:    "source-documents-6asblvno",
	envEndpoint:  "https://t3.storageapi.dev",
	envRegion:    "auto",
	envAccessKey: "tid_test_access_key",
	envSecretKey: "tsec_test_secret_key",
}

var documentEnvKeys = []string{envBucket, envEndpoint, envRegion, envAccessKey, envSecretKey}

func setAllDocumentEnv(t *testing.T) {
	t.Helper()
	for _, k := range documentEnvKeys {
		t.Setenv(k, documentEnvValues[k])
	}
}

// t.Setenv FIRST so its cleanup restores the runner's value, THEN Unsetenv —
// t.Setenv(k, "") tests EMPTY, a different string, and leaves absence unproven.
func unsetDocumentEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "sentinel")
	os.Unsetenv(key)
	if got := os.Getenv(key); got != "" {
		t.Fatalf("test setup: %s = %q after Unsetenv, want absent", key, got)
	}
}

func TestConfigFromEnv_AllSetSucceeds(t *testing.T) {
	setAllDocumentEnv(t)

	cfg, err := document.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv() with all five DOCUMENT_* set returned unexpected error: %v", err)
	}

	want := document.Config{
		Bucket:          documentEnvValues[envBucket],
		Endpoint:        documentEnvValues[envEndpoint],
		Region:          documentEnvValues[envRegion],
		AccessKeyID:     documentEnvValues[envAccessKey],
		SecretAccessKey: documentEnvValues[envSecretKey],
	}
	if cfg != want {
		t.Errorf("ConfigFromEnv() = %+v, want %+v", cfg, want)
	}
}

// AC-2's "unset or empty". Both columns reach the SAME branch (plain
// os.Getenv + == ""); they are not independent behaviours, and the table says
// so rather than pretending otherwise.
func TestConfigFromEnv_MissingEachVarErrors(t *testing.T) {
	for _, key := range documentEnvKeys {
		t.Run(key, func(t *testing.T) {
			setAllDocumentEnv(t)
			unsetDocumentEnv(t, key)

			cfg, err := document.ConfigFromEnv()
			if err == nil {
				t.Fatalf("ConfigFromEnv() with %s unset = (%+v, nil), want an error", key, cfg)
			}
			// Absence has no offending value to quote (unlike
			// submission.RateLimitConfigFromEnv's malformed-value errors), so
			// naming the variable is the whole requirement.
			if !strings.Contains(err.Error(), key) {
				t.Errorf("ConfigFromEnv() with %s unset error = %q, want it to name the variable %s",
					key, err.Error(), key)
			}
			if cfg != (document.Config{}) {
				t.Errorf("ConfigFromEnv() with %s unset returned %+v, want the zero Config", key, cfg)
			}
		})
	}
}

func TestConfigFromEnv_EmptyValueErrors(t *testing.T) {
	for _, key := range documentEnvKeys {
		t.Run(key, func(t *testing.T) {
			setAllDocumentEnv(t)
			t.Setenv(key, "")

			cfg, err := document.ConfigFromEnv()
			if err == nil {
				t.Fatalf("ConfigFromEnv() with %s=\"\" = (%+v, nil), want an error — empty is not \"defaulted\"", key, cfg)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("ConfigFromEnv() with %s=\"\" error = %q, want it to name the variable %s",
					key, err.Error(), key)
			}
			if cfg != (document.Config{}) {
				t.Errorf("ConfigFromEnv() with %s=\"\" returned %+v, want the zero Config", key, cfg)
			}
		})
	}
}

// The LAST variable is the missing one, so an implementation that populates
// the struct as it reads and returns it alongside the error is caught with
// four fields already filled. Unsetting the first would let such an
// implementation pass on an accidentally-zero Config.
func TestConfigFromEnv_ErrorReturnsZeroConfig(t *testing.T) {
	setAllDocumentEnv(t)
	unsetDocumentEnv(t, envSecretKey)

	cfg, err := document.ConfigFromEnv()
	if err == nil {
		t.Fatalf("ConfigFromEnv() with %s unset = (%+v, nil), want an error", envSecretKey, cfg)
	}
	if cfg != (document.Config{}) {
		t.Fatalf("ConfigFromEnv() error path returned %+v, want the zero Config — a partially populated "+
			"Config invites a caller that ignores the error", cfg)
	}
}

// --- wire test doubles ---------------------------------------------------

// fakeRoundTripper records the outbound request (count + last-seen) and
// answers with a canned response.
type fakeRoundTripper struct {
	calls    int
	lastReq  *http.Request
	lastBody []byte

	// onDispatch runs before the request is recorded — lets a test observe how
	// much of the source body was consumed before transport began.
	onDispatch func()

	respond func(*http.Request) *http.Response
}

func (f *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if f.onDispatch != nil {
		f.onDispatch()
	}
	f.calls++
	f.lastReq = req
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		f.lastBody = b
	}
	return f.respond(req), nil
}

// fakeBody is a ReadSeeker that reports how many bytes have been read out of
// it — a store that buffers with io.ReadAll before calling the SDK shows a
// full count before the transport is entered.
type fakeBody struct {
	*bytes.Reader
	consumed int
}

func newFakeBody(n int) *fakeBody {
	return &fakeBody{Reader: bytes.NewReader(bytes.Repeat([]byte("q"), n))}
}

func (f *fakeBody) Read(p []byte) (int, error) {
	n, err := f.Reader.Read(p)
	f.consumed += n
	return n, err
}

// fakeReadCloser reports whether anything read from it — Get must hand back an
// UNREAD stream (AC-4), never a buffered-and-rewrapped copy.
type fakeReadCloser struct {
	r          io.Reader
	readCalled bool
	closed     bool
}

func (f *fakeReadCloser) Read(p []byte) (int, error) { f.readCalled = true; return f.r.Read(p) }
func (f *fakeReadCloser) Close() error               { f.closed = true; return nil }

// The bucket must be a valid 3-63 character DNS label or the SDK falls back to
// path style on its own — measured: a 1-2 character bucket, an uppercase one,
// or one containing a dot all yield https://<endpoint>/<bucket>/<key>
// regardless of UsePathStyle. Testing with a toy bucket name would therefore
// stage a failure no implementation can fix.
const (
	wireBucket   = "source-documents-6asblvno"
	wireEndpoint = "https://t3.storageapi.dev"
	wireHost     = "source-documents-6asblvno.t3.storageapi.dev"
	wireKey      = "tenants/" + keyTenantA + "/" + keyHash
)

func newWireStore(t *testing.T, rt *fakeRoundTripper) document.ObjectStore {
	t.Helper()
	store, err := document.NewS3Store(document.Config{
		Bucket:          wireBucket,
		Endpoint:        wireEndpoint,
		Region:          "auto",
		AccessKeyID:     "tid_test_access_key",
		SecretAccessKey: "tsec_test_secret_key",
	}, &http.Client{Transport: rt})
	if err != nil {
		t.Fatalf("NewS3Store() returned unexpected error: %v", err)
	}
	return store
}

// okBody answers 200 with n bytes and no Content-Range.
func okBody(n int) func(*http.Request) *http.Response {
	return func(req *http.Request) *http.Response {
		return newFakeResponse(req, http.StatusOK, nil, strings.Repeat("x", n))
	}
}

func newFakeResponse(req *http.Request, status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	header.Set("Content-Length", strconv.Itoa(len(body)))
	return &http.Response{
		StatusCode:    status,
		Header:        header,
		ContentLength: int64(len(body)),
		Body:          io.NopCloser(strings.NewReader(body)),
		Request:       req,
	}
}

// --- S3Store wire behaviour (AC-4, AC-5) ---------------------------------

// [no-path-style]: the bucket is a subdomain of the configured endpoint, and
// the path carries the key alone.
func TestS3Store_UsesVirtualHostStyle(t *testing.T) {
	rt := &fakeRoundTripper{respond: okBody(3)}
	store := newWireStore(t, rt)

	obj, err := store.Get(context.Background(), wireKey, "")
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	_ = obj.Body.Close()

	if rt.calls != 1 {
		t.Fatalf("Get() dispatched %d requests, want exactly 1", rt.calls)
	}
	if got := rt.lastReq.URL.Host; got != wireHost {
		t.Errorf("Get() request host = %q, want %q — the bucket belongs in the hostname, not the path", got, wireHost)
	}
	if got, want := rt.lastReq.URL.Path, "/"+wireKey; got != want {
		t.Errorf("Get() request path = %q, want %q", got, want)
	}
	if strings.HasPrefix(rt.lastReq.URL.Path, "/"+wireBucket) {
		t.Errorf("Get() request path = %q — starts with the bucket, so UsePathStyle is set", rt.lastReq.URL.Path)
	}
}

// AC-4. The second row is the discriminator: with no ContentLength forwarded
// the SDK derives one from the seeker, so an implementation that drops `size`
// still shows 1234 on a 1234-byte body. Only a body LONGER than `size` proves
// the parameter is used.
func TestS3Store_PutSendsExactContentLength(t *testing.T) {
	for _, tc := range []struct {
		name         string
		bodyLen      int
		size         int64
		wantWireBody bool
	}{
		{"exact", 1234, 1234, true},
		{"size is authoritative over the reader's length", 2000, 1234, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := newFakeBody(tc.bodyLen)
			consumedAtDispatch := -1
			rt := &fakeRoundTripper{
				onDispatch: func() { consumedAtDispatch = body.consumed },
				respond: func(req *http.Request) *http.Response {
					return newFakeResponse(req, http.StatusOK, http.Header{"ETag": {`"deadbeef"`}}, "")
				},
			}
			store := newWireStore(t, rt)

			if err := store.Put(context.Background(), wireKey, body, tc.size); err != nil {
				t.Fatalf("Put() returned unexpected error: %v", err)
			}

			if rt.calls != 1 {
				t.Fatalf("Put() dispatched %d requests, want exactly 1", rt.calls)
			}
			if got := rt.lastReq.Method; got != http.MethodPut {
				t.Errorf("Put() method = %q, want %q", got, http.MethodPut)
			}
			if got, want := rt.lastReq.URL.Path, "/"+wireKey; got != want {
				t.Errorf("Put() request path = %q, want %q", got, want)
			}
			if rt.lastReq.ContentLength != tc.size {
				t.Errorf("Put(size=%d) sent Content-Length %d, want %d", tc.size, rt.lastReq.ContentLength, tc.size)
			}
			if consumedAtDispatch != 0 {
				t.Errorf("Put() had already read %d of %d body bytes when the transport was entered, want 0 — "+
					"the body must stream, not be buffered into memory first", consumedAtDispatch, tc.bodyLen)
			}
			if tc.wantWireBody {
				if got := len(rt.lastBody); got != tc.bodyLen {
					t.Errorf("Put() put %d bytes on the wire, want %d", got, tc.bodyLen)
				}
				if !bytes.Equal(rt.lastBody, bytes.Repeat([]byte("q"), tc.bodyLen)) {
					t.Errorf("Put() wire body does not match the source bytes")
				}
			}
		})
	}
}

// AC-5. No range parser here: the inbound string goes to the wire verbatim.
func TestS3Store_GetForwardsRangeHeader(t *testing.T) {
	const rangeHeader = "bytes=10-19"

	rt := &fakeRoundTripper{respond: func(req *http.Request) *http.Response {
		return newFakeResponse(req, http.StatusPartialContent,
			http.Header{"Content-Range": {"bytes 10-19/1000"}}, "0123456789")
	}}
	store := newWireStore(t, rt)

	obj, err := store.Get(context.Background(), wireKey, rangeHeader)
	if err != nil {
		t.Fatalf("Get(range=%q) returned unexpected error: %v", rangeHeader, err)
	}
	_ = obj.Body.Close()

	if rt.calls != 1 {
		t.Fatalf("Get() dispatched %d requests, want exactly 1", rt.calls)
	}
	if got := rt.lastReq.Header.Get("Range"); got != rangeHeader {
		t.Errorf("Get(range=%q) sent Range: %q, want it forwarded verbatim", rangeHeader, got)
	}
}

// AC-5. aws.String("") puts an EMPTY Range header on the wire (measured), so
// the store must leave GetObjectInput.Range nil, never a pointer to "".
func TestS3Store_GetEmptyRangeSendsNoRangeHeader(t *testing.T) {
	rt := &fakeRoundTripper{respond: okBody(1000)}
	store := newWireStore(t, rt)

	obj, err := store.Get(context.Background(), wireKey, "")
	if err != nil {
		t.Fatalf("Get(range=\"\") returned unexpected error: %v", err)
	}
	_ = obj.Body.Close()

	// The positive half: a store that never dispatched would trivially have no
	// Range header.
	if rt.calls != 1 {
		t.Fatalf("Get() dispatched %d requests, want exactly 1", rt.calls)
	}
	if got, ok := rt.lastReq.Header["Range"]; ok {
		t.Errorf("Get(range=\"\") sent Range: %q, want the header absent entirely", got)
	}
}

// AC-5 + G3/G4. GetObjectOutput exposes no HTTP status, so Partial is derived
// from ContentRange; Size is the byte count of Body, NOT the object's full
// size (10 vs 1000 here) — DOC-01-05 reads Size to build its own response.
func TestS3Store_GetPartialSetsContentRange(t *testing.T) {
	t.Run("206 partial", func(t *testing.T) {
		payload := "0123456789"
		src := &fakeReadCloser{r: strings.NewReader(payload)}
		rt := &fakeRoundTripper{respond: func(req *http.Request) *http.Response {
			resp := newFakeResponse(req, http.StatusPartialContent,
				http.Header{"Content-Range": {"bytes 10-19/1000"}}, payload)
			resp.Body = src
			return resp
		}}
		store := newWireStore(t, rt)

		obj, err := store.Get(context.Background(), wireKey, "bytes=10-19")
		if err != nil {
			t.Fatalf("Get() on a 206 returned unexpected error: %v", err)
		}

		if !obj.Partial {
			t.Errorf("Get() on a 206 with Content-Range = Object.Partial false, want true")
		}
		if got, want := obj.ContentRange, "bytes 10-19/1000"; got != want {
			t.Errorf("Get() Object.ContentRange = %q, want %q mirrored from the response", got, want)
		}
		if got, want := obj.Size, int64(len(payload)); got != want {
			t.Errorf("Get() Object.Size = %d, want %d (the byte count of Body, not the object's full 1000)", got, want)
		}
		if src.readCalled {
			t.Errorf("Get() consumed the response body before returning, want an UNREAD io.ReadCloser")
		}

		got, err := io.ReadAll(obj.Body)
		if err != nil {
			t.Fatalf("read Object.Body: %v", err)
		}
		if string(got) != payload {
			t.Errorf("Object.Body = %q, want %q", got, payload)
		}
		_ = obj.Body.Close()
	})

	// Control: without it, hardcoding Partial: true passes the row above.
	t.Run("200 full", func(t *testing.T) {
		rt := &fakeRoundTripper{respond: okBody(1000)}
		store := newWireStore(t, rt)

		obj, err := store.Get(context.Background(), wireKey, "")
		if err != nil {
			t.Fatalf("Get() on a 200 returned unexpected error: %v", err)
		}
		defer func() { _ = obj.Body.Close() }()

		if obj.Partial {
			t.Errorf("Get() on a 200 with no Content-Range = Object.Partial true, want false")
		}
		if obj.ContentRange != "" {
			t.Errorf("Get() on a 200 = Object.ContentRange %q, want \"\"", obj.ContentRange)
		}
		if got, want := obj.Size, int64(1000); got != want {
			t.Errorf("Get() on a 200 = Object.Size %d, want %d", got, want)
		}
	})
}

const rangeErrXML = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<Error><Code>InvalidRange</Code><Message>The requested range is not satisfiable</Message></Error>`

const noSuchKeyXML = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`

const accessDeniedXML = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`

// AC-5. Detection keys on the HTTP STATUS, not on an error string: Tigris need
// not emit AWS's literal. Measured — an XML-bodied 416 yields APIError code
// "InvalidRange", a bodiless 416 yields "RequestedRangeNotSatisfiable"; only
// the status is common to both. The 404 and 500 rows are what make
// "distinguishable" mean something: a store that returns the sentinel for
// every failure passes the 416 rows alone.
func TestS3Store_GetUnsatisfiableRangeIsDistinguishable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"416 with AWS's InvalidRange body", http.StatusRequestedRangeNotSatisfiable, rangeErrXML, document.ErrRangeNotSatisfiable},
		{"416 with no body", http.StatusRequestedRangeNotSatisfiable, "", document.ErrRangeNotSatisfiable},
		{"404 NoSuchKey", http.StatusNotFound, noSuchKeyXML, document.ErrNotFound},
		// 403 rather than 500: the SDK retries 5xx with backoff (~5s per case),
		// and a private bucket really does answer 403.
		{"403 AccessDenied", http.StatusForbidden, accessDeniedXML, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeRoundTripper{respond: func(req *http.Request) *http.Response {
				return newFakeResponse(req, tc.status, http.Header{"Content-Type": {"application/xml"}}, tc.body)
			}}
			store := newWireStore(t, rt)

			obj, err := store.Get(context.Background(), wireKey, "bytes=99999-")
			if err == nil {
				if obj.Body != nil {
					_ = obj.Body.Close()
				}
				t.Fatalf("Get() on a %d = (%+v, nil), want an error", tc.status, obj)
			}

			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("Get() on a %d error = %v, want it to wrap %v", tc.status, err, tc.want)
			}
			if tc.want != document.ErrRangeNotSatisfiable && errors.Is(err, document.ErrRangeNotSatisfiable) {
				t.Errorf("Get() on a %d wraps ErrRangeNotSatisfiable, want it NOT to — the handler would answer "+
					"416 for a failure that is not a range problem", tc.status)
			}
			if tc.want != document.ErrNotFound && errors.Is(err, document.ErrNotFound) {
				t.Errorf("Get() on a %d wraps ErrNotFound, want it NOT to", tc.status)
			}
		})
	}
}

// main.go wires nil (the invoice.NewValidator idiom), so the production path
// is the one with no injected client.
func TestNewS3Store_NilHTTPClientIsAccepted(t *testing.T) {
	store, err := document.NewS3Store(document.Config{
		Bucket:          wireBucket,
		Endpoint:        wireEndpoint,
		Region:          "auto",
		AccessKeyID:     "tid_test_access_key",
		SecretAccessKey: "tsec_test_secret_key",
	}, nil)
	if err != nil {
		t.Fatalf("NewS3Store(cfg, nil) returned unexpected error: %v", err)
	}
	if store == nil {
		t.Fatal("NewS3Store(cfg, nil) = nil store, want a client built on a default http.Client")
	}
}

// --- structural pins ------------------------------------------------------

// AC-4's streaming contract is a signature, not a runtime observation: a
// RoundTripper cannot see whether Put buffered before handing over. Swapping
// io.ReadSeeker for []byte, or io.ReadCloser for []byte, breaks compilation
// here.
type fakeObjectStore struct{}

func (fakeObjectStore) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	return nil
}

func (fakeObjectStore) Get(ctx context.Context, key, rangeHeader string) (document.Object, error) {
	return document.Object{}, nil
}

var (
	_ document.ObjectStore                                              = (*fakeObjectStore)(nil)
	_ func(document.Config, *http.Client) (document.ObjectStore, error) = document.NewS3Store
)
