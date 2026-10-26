package document_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func s3ErrorXML(code, msg string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + code + `</Code><Message>` + msg + `</Message></Error>`
}

// scripted answers with replies[i] on call i+1 and the last reply after that.
func scripted(rt *fakeRoundTripper, replies ...func(*http.Request) *http.Response) {
	rt.respond = func(req *http.Request) *http.Response {
		i := rt.calls - 1
		if i >= len(replies) {
			i = len(replies) - 1
		}
		return replies[i](req)
	}
}

func errReply(status int, code, msg string) func(*http.Request) *http.Response {
	return func(req *http.Request) *http.Response {
		return newFakeResponse(req, status, http.Header{"Content-Type": {"application/xml"}}, s3ErrorXML(code, msg))
	}
}

func wantGetAfterRetry(t *testing.T, rt *fakeRoundTripper) {
	t.Helper()
	obj, err := newWireStore(t, rt).Get(context.Background(), wireKey, "")
	if err != nil {
		t.Fatalf("Get() = %v, want the retry to recover", err)
	}
	defer obj.Body.Close()
	if obj.Size != 5 {
		t.Errorf("Object.Size = %d, want 5", obj.Size)
	}
	if rt.calls != 2 {
		t.Errorf("RoundTrip calls = %d, want 2", rt.calls)
	}
}

func TestS3Store_GetRetries408RequestCanceled(t *testing.T) {
	rt := &fakeRoundTripper{}
	scripted(rt, errReply(408, "RequestCanceled", "Request is canceled."), okBody(5))
	wantGetAfterRetry(t, rt)
}

func TestS3Store_GetRetries408WithAnotherCode(t *testing.T) {
	rt := &fakeRoundTripper{}
	scripted(rt, errReply(408, "ClientTimeout", "timed out"), okBody(5))
	wantGetAfterRetry(t, rt)
}

func TestS3Store_GetRetriesRequestCanceledCode(t *testing.T) {
	rt := &fakeRoundTripper{}
	scripted(rt, errReply(400, "RequestCanceled", "Request is canceled."), okBody(5))
	wantGetAfterRetry(t, rt)
}

func TestS3Store_GetStillRetries503(t *testing.T) {
	rt := &fakeRoundTripper{}
	scripted(rt, func(req *http.Request) *http.Response {
		return newFakeResponse(req, http.StatusServiceUnavailable, nil, "")
	}, okBody(5))
	wantGetAfterRetry(t, rt)
}

func TestS3Store_GetCallerCancelIsNotRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	hc := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			cancel()
			return nil, context.Canceled
		}
		return newFakeResponse(req, http.StatusOK, nil, "hello"), nil
	})}
	store := newStoreWithClient(t, hc)

	_, err := store.Get(ctx, wireKey, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Get() error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("RoundTrip calls = %d, want 1", calls)
	}
}

func TestS3Store_GetCallerCancelAfter408IsNotRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	hc := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			cancel()
			return errReply(408, "RequestCanceled", "Request is canceled.")(req), nil
		}
		return newFakeResponse(req, http.StatusOK, nil, "hello"), nil
	})}
	store := newStoreWithClient(t, hc)

	if _, err := store.Get(ctx, wireKey, ""); err == nil {
		t.Fatal("Get() = nil error, want the caller's cancel to stop the retry")
	}
	if calls != 1 {
		t.Errorf("RoundTrip calls = %d, want 1", calls)
	}
}

func TestS3Store_PutDoesNotRetry408RequestCanceled(t *testing.T) {
	rt := &fakeRoundTripper{}
	scripted(rt, errReply(408, "RequestCanceled", "Request is canceled."), okBody(0))

	err := newWireStore(t, rt).Put(context.Background(), wireKey, bytes.NewReader([]byte("hello")), 5)
	if err == nil {
		t.Fatal("Put() = nil error, want the 408 surfaced")
	}
	if rt.calls != 1 {
		t.Errorf("RoundTrip calls = %d, want 1", rt.calls)
	}
}

func TestS3Store_GetStopsAfterThreeAttempts(t *testing.T) {
	rt := &fakeRoundTripper{}
	scripted(rt, errReply(408, "RequestCanceled", "Request is canceled."))

	if _, err := newWireStore(t, rt).Get(context.Background(), wireKey, ""); err == nil {
		t.Fatal("Get() = nil error, want the failure after the last attempt")
	}
	if rt.calls != 3 {
		t.Errorf("RoundTrip calls = %d, want 3", rt.calls)
	}
}

func TestS3Store_GetNotFoundIsNotRetried(t *testing.T) {
	rt := &fakeRoundTripper{}
	scripted(rt, errReply(404, "NoSuchKey", "The specified key does not exist."), okBody(5))

	_, err := newWireStore(t, rt).Get(context.Background(), wireKey, "")
	if !errors.Is(err, document.ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
	if rt.calls != 1 {
		t.Errorf("RoundTrip calls = %d, want 1", rt.calls)
	}
}

func newStoreWithClient(t *testing.T, hc *http.Client) document.ObjectStore {
	t.Helper()
	store, err := document.NewS3Store(document.Config{
		Bucket:          wireBucket,
		Endpoint:        wireEndpoint,
		Region:          "auto",
		AccessKeyID:     "tid_test_access_key",
		SecretAccessKey: "tsec_test_secret_key",
	}, hc)
	if err != nil {
		t.Fatalf("NewS3Store() returned unexpected error: %v", err)
	}
	return store
}
