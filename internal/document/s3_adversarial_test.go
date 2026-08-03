// s3_adversarial_test.go: QA Mode B coverage beyond the AC specs — credential
// hygiene, context cancellation, and the Put body's edge shapes.
package document_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

// Distinct from s3_test.go's wire credentials so a match below is unambiguous.
const (
	sentinelAccessKeyID     = "AKIA-QA-SENTINEL-KEY-ID"
	sentinelSecretAccessKey = "QA-SENTINEL-SECRET-ACCESS-KEY-VALUE"
)

func newSentinelStore(t *testing.T, rt *fakeRoundTripper) document.ObjectStore {
	t.Helper()
	store, err := document.NewS3Store(document.Config{
		Bucket:          wireBucket,
		Endpoint:        wireEndpoint,
		Region:          "auto",
		AccessKeyID:     sentinelAccessKeyID,
		SecretAccessKey: sentinelSecretAccessKey,
	}, &http.Client{Transport: rt})
	if err != nil {
		t.Fatalf("NewS3Store() returned unexpected error: %v", err)
	}
	return store
}

// requestText renders the recorded request's URL, headers and body for a
// substring sweep.
func requestText(t *testing.T, rt *fakeRoundTripper) string {
	t.Helper()
	if rt.lastReq == nil {
		t.Fatal("no request was recorded")
	}
	var b strings.Builder
	b.WriteString(rt.lastReq.URL.String())
	b.WriteString("\n")
	for k, vs := range rt.lastReq.Header {
		b.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
	}
	b.Write(rt.lastBody)
	return b.String()
}

// SigV4 signs with a derived key, so the secret must never be transmitted. The
// access-key id assertion is the non-vacuity control: it proves the sweep
// actually reads headers and that the request was signed at all.
func TestS3Store_SecretAccessKeyNeverReachesTheWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(document.ObjectStore) error
	}{
		{"Get", func(s document.ObjectStore) error {
			obj, err := s.Get(context.Background(), wireKey, "bytes=0-9")
			if obj.Body != nil {
				_ = obj.Body.Close()
			}
			return err
		}},
		{"Put", func(s document.ObjectStore) error {
			return s.Put(context.Background(), wireKey, bytes.NewReader([]byte("payload")), 7)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeRoundTripper{respond: func(req *http.Request) *http.Response {
				return newFakeResponse(req, http.StatusOK, nil, "")
			}}
			if err := tc.call(newSentinelStore(t, rt)); err != nil {
				t.Fatalf("%s() returned unexpected error: %v", tc.name, err)
			}

			text := requestText(t, rt)
			if !strings.Contains(text, sentinelAccessKeyID) {
				t.Fatalf("%s() request carries no access-key id — it was not SigV4-signed, so "+
					"'the secret is absent' below is vacuous. request = %q", tc.name, text)
			}
			if strings.Contains(text, sentinelSecretAccessKey) {
				t.Errorf("%s() put the secret access key on the wire; SigV4 signs with a derived key and must "+
					"never transmit it", tc.name)
			}
		})
	}
}

// A store error is logged and often returned to an operator, so it must name
// the key and never the credential.
func TestS3Store_ErrorsNeverEchoTheSecretAccessKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(document.ObjectStore) error
	}{
		{"Get", func(s document.ObjectStore) error {
			_, err := s.Get(context.Background(), wireKey, "")
			return err
		}},
		{"Put", func(s document.ObjectStore) error {
			return s.Put(context.Background(), wireKey, bytes.NewReader([]byte("x")), 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeRoundTripper{respond: func(req *http.Request) *http.Response {
				return newFakeResponse(req, http.StatusForbidden,
					http.Header{"Content-Type": {"application/xml"}}, accessDeniedXML)
			}}

			err := tc.call(newSentinelStore(t, rt))
			if err == nil {
				t.Fatalf("%s() on a 403 returned nil, so the hygiene assertions below are vacuous", tc.name)
			}
			if !strings.Contains(err.Error(), wireKey) {
				t.Errorf("%s() error = %q, want it to name the key %q — an operator cannot act on an "+
					"unattributed store failure", tc.name, err, wireKey)
			}
			if strings.Contains(err.Error(), sentinelSecretAccessKey) {
				t.Errorf("%s() error echoed the secret access key", tc.name)
			}
			if strings.Contains(err.Error(), sentinelAccessKeyID) {
				t.Errorf("%s() error echoed the access key id", tc.name)
			}
		})
	}
}

// A cancelled request is a caller that went away, not a missing object or a bad
// range — mapping it onto either sentinel would answer 404/416 for a hang-up.
func TestS3Store_CancelledContextIsNotAStoreDefect(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(document.ObjectStore, context.Context) (document.Object, error)
	}{
		{"Get", func(s document.ObjectStore, ctx context.Context) (document.Object, error) {
			return s.Get(ctx, wireKey, "bytes=0-9")
		}},
		{"Put", func(s document.ObjectStore, ctx context.Context) (document.Object, error) {
			return document.Object{}, s.Put(ctx, wireKey, bytes.NewReader([]byte("x")), 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeRoundTripper{respond: okBody(10)}
			store := newSentinelStore(t, rt)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			obj, err := tc.call(store, ctx)
			if err == nil {
				t.Fatalf("%s() on a cancelled context returned nil, want an error", tc.name)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s() on a cancelled context error = %v, want it to wrap context.Canceled", tc.name, err)
			}
			if errors.Is(err, document.ErrNotFound) {
				t.Errorf("%s() on a cancelled context wraps ErrNotFound — a hang-up would be reported as 404", tc.name)
			}
			if errors.Is(err, document.ErrRangeNotSatisfiable) {
				t.Errorf("%s() on a cancelled context wraps ErrRangeNotSatisfiable", tc.name)
			}
			if rt.calls != 0 {
				t.Errorf("%s() dispatched %d requests on a cancelled context, want 0", tc.name, rt.calls)
			}
			if obj.Body != nil {
				_ = obj.Body.Close()
				t.Errorf("%s() returned a non-nil Body alongside an error", tc.name)
			}
		})
	}
}

// A zero-byte upload is a legitimate object, not an error, and it must still
// declare Content-Length: 0 rather than fall back to chunked.
func TestS3Store_PutZeroByteBody(t *testing.T) {
	rt := &fakeRoundTripper{respond: func(req *http.Request) *http.Response {
		return newFakeResponse(req, http.StatusOK, http.Header{"ETag": {`"d41d8cd9"`}}, "")
	}}
	store := newSentinelStore(t, rt)

	if err := store.Put(context.Background(), wireKey, bytes.NewReader(nil), 0); err != nil {
		t.Fatalf("Put() of a zero-byte body returned unexpected error: %v", err)
	}
	if rt.calls != 1 {
		t.Fatalf("Put() dispatched %d requests, want exactly 1", rt.calls)
	}
	if rt.lastReq.ContentLength != 0 {
		t.Errorf("Put(size=0) sent Content-Length %d, want 0", rt.lastReq.ContentLength)
	}
	if len(rt.lastBody) != 0 {
		t.Errorf("Put(size=0) put %d bytes on the wire, want 0", len(rt.lastBody))
	}
}

// The seam's contract: Put transmits from the reader's CURRENT offset and never
// rewinds. A caller that consumed the reader first (hashing it, say) must seek
// back itself — the declared size and the remaining bytes have to agree or the
// real transport rejects the request ("http: ContentLength=N with Body length M").
func TestS3Store_PutTransmitsFromTheReadersCurrentOffset(t *testing.T) {
	src := bytes.NewReader([]byte("0123456789"))
	if _, err := src.Seek(5, io.SeekStart); err != nil {
		t.Fatalf("test setup: seek: %v", err)
	}

	rt := &fakeRoundTripper{respond: func(req *http.Request) *http.Response {
		return newFakeResponse(req, http.StatusOK, nil, "")
	}}
	store := newSentinelStore(t, rt)

	if err := store.Put(context.Background(), wireKey, src, 5); err != nil {
		t.Fatalf("Put() returned unexpected error: %v", err)
	}
	if got, want := string(rt.lastBody), "56789"; got != want {
		t.Errorf("Put() wire body = %q, want %q — Put must not rewind the reader", got, want)
	}
	if rt.lastReq.ContentLength != 5 {
		t.Errorf("Put() sent Content-Length %d, want 5", rt.lastReq.ContentLength)
	}
}

// On failure Get must return the zero Object: a non-nil Body alongside an error
// is a stream nobody closes.
func TestS3Store_GetErrorReturnsTheZeroObject(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"403 AccessDenied", http.StatusForbidden, accessDeniedXML},
		{"404 NoSuchKey", http.StatusNotFound, noSuchKeyXML},
		{"416 InvalidRange", http.StatusRequestedRangeNotSatisfiable, rangeErrXML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeRoundTripper{respond: func(req *http.Request) *http.Response {
				return newFakeResponse(req, tc.status,
					http.Header{"Content-Type": {"application/xml"}}, tc.body)
			}}
			store := newSentinelStore(t, rt)

			obj, err := store.Get(context.Background(), wireKey, "bytes=0-9")
			if err == nil {
				t.Fatalf("Get() on a %d returned nil error", tc.status)
			}
			if obj.Body != nil {
				_ = obj.Body.Close()
				t.Errorf("Get() on a %d returned a non-nil Body alongside an error", tc.status)
			}
			if obj.Size != 0 || obj.ContentRange != "" || obj.Partial {
				t.Errorf("Get() on a %d returned %+v, want the zero Object — a caller that ignores the "+
					"error must not read a Partial/ContentRange it can act on", tc.status, obj)
			}
		})
	}
}
