package document_test

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>`

func xmlRT(status int, body string) *fakeRoundTripper {
	return &fakeRoundTripper{respond: func(req *http.Request) *http.Response {
		return newFakeResponse(req, status, http.Header{"Content-Type": {"application/xml"}}, body)
	}}
}

func TestS3Store_ListSendsListObjectsV2ForThePrefix(t *testing.T) {
	prefix := "tenants/" + keyTenantA + "/"
	rt := xmlRT(http.StatusOK, xmlHeader+`<ListBucketResult><Name>b</Name><IsTruncated>false</IsTruncated>`+
		`<Contents><Key>k1</Key></Contents><Contents><Key>k2</Key></Contents></ListBucketResult>`)

	keys, err := newWireStore(t, rt).List(context.Background(), prefix)
	if err != nil {
		t.Fatalf("List() returned unexpected error: %v", err)
	}
	if len(keys) != 2 || keys[0] != "k1" || keys[1] != "k2" {
		t.Errorf("List() = %v, want [k1 k2]", keys)
	}
	if rt.calls != 1 || rt.lastReq.Method != http.MethodGet || rt.lastReq.URL.Host != wireHost {
		t.Errorf("List() sent %d %s to %q, want 1 GET to %q", rt.calls, rt.lastReq.Method, rt.lastReq.URL.Host, wireHost)
	}
	q := rt.lastReq.URL.Query()
	if q.Get("list-type") != "2" || q.Get("prefix") != prefix || q.Get("max-keys") != "1000" {
		t.Errorf("List() query = %v, want list-type=2 prefix=%s max-keys=1000", q, prefix)
	}
}

func TestS3Store_ListOfAnEmptyPrefixIsEmpty(t *testing.T) {
	rt := xmlRT(http.StatusOK, xmlHeader+`<ListBucketResult><Name>b</Name></ListBucketResult>`)
	keys, err := newWireStore(t, rt).List(context.Background(), "tenants/none/")
	if err != nil {
		t.Fatalf("List() returned unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("List() = %v, want empty", keys)
	}
}

func TestS3Store_ListFailureNamesThePrefix(t *testing.T) {
	prefix := "tenants/" + keyTenantA + "/"
	_, err := newWireStore(t, xmlRT(http.StatusForbidden, accessDeniedXML)).List(context.Background(), prefix)
	if err == nil || !strings.Contains(err.Error(), prefix) {
		t.Errorf("List() on a 403 error = %v, want it to name %q", err, prefix)
	}
}

func TestS3Store_DeleteManySendsOneQuietDeleteObjects(t *testing.T) {
	rt := xmlRT(http.StatusOK, xmlHeader+`<DeleteResult/>`)
	if err := newWireStore(t, rt).DeleteMany(context.Background(), []string{"k1", "k2"}); err != nil {
		t.Fatalf("DeleteMany() returned unexpected error: %v", err)
	}
	if rt.calls != 1 || rt.lastReq.Method != http.MethodPost || rt.lastReq.URL.Host != wireHost {
		t.Fatalf("DeleteMany() sent %d %s to %q, want 1 POST to %q", rt.calls, rt.lastReq.Method, rt.lastReq.URL.Host, wireHost)
	}
	if _, ok := rt.lastReq.URL.Query()["delete"]; !ok {
		t.Errorf("DeleteMany() query = %v, want the delete subresource", rt.lastReq.URL.Query())
	}
	body := string(rt.lastBody)
	for _, want := range []string{"<Key>k1</Key>", "<Key>k2</Key>", "<Quiet>true</Quiet>"} {
		if !strings.Contains(body, want) {
			t.Errorf("DeleteMany() body = %q, want it to contain %s", body, want)
		}
	}
}

func TestS3Store_DeleteManyOfNoKeysSendsNothing(t *testing.T) {
	rt := xmlRT(http.StatusOK, xmlHeader+`<DeleteResult/>`)
	if err := newWireStore(t, rt).DeleteMany(context.Background(), nil); err != nil {
		t.Fatalf("DeleteMany(nil) returned unexpected error: %v", err)
	}
	if rt.calls != 0 {
		t.Errorf("DeleteMany(nil) sent %d requests, want 0", rt.calls)
	}
}

func TestS3Store_DeleteManyRefusesMoreThanOneThousandKeys(t *testing.T) {
	keys := make([]string, 1001)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}
	rt := xmlRT(http.StatusOK, xmlHeader+`<DeleteResult/>`)
	store := newWireStore(t, rt)
	if err := store.DeleteMany(context.Background(), keys); err == nil {
		t.Error("DeleteMany(1001 keys) returned nil, want an error")
	}
	if rt.calls != 0 {
		t.Errorf("DeleteMany(1001 keys) sent %d requests, want 0", rt.calls)
	}
	if err := store.DeleteMany(context.Background(), keys[:1000]); err != nil {
		t.Fatalf("DeleteMany(1000 keys) returned unexpected error: %v", err)
	}
	if rt.calls != 1 {
		t.Errorf("DeleteMany(1000 keys) sent %d requests, want 1", rt.calls)
	}
}

func TestS3Store_DeleteManyReportsAPerKeyError(t *testing.T) {
	rt := xmlRT(http.StatusOK, xmlHeader+
		`<DeleteResult><Error><Key>k2</Key><Code>AccessDenied</Code><Message>no</Message></Error></DeleteResult>`)
	err := newWireStore(t, rt).DeleteMany(context.Background(), []string{"k1", "k2"})
	if err == nil || !strings.Contains(err.Error(), "k2") || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("DeleteMany() error = %v, want it to name k2 and AccessDenied", err)
	}
}

func TestS3Store_DeleteManyFailsOnARefusedRequest(t *testing.T) {
	rt := xmlRT(http.StatusForbidden, accessDeniedXML)
	err := newWireStore(t, rt).DeleteMany(context.Background(), []string{"k1", "k2"})
	if err == nil || !strings.Contains(err.Error(), "k1") || !strings.Contains(err.Error(), "delete 2 keys") {
		t.Errorf("DeleteMany() on a 403 error = %v, want it to name k1 and %q", err, "delete 2 keys")
	}
	if rt.calls != 1 {
		t.Errorf("DeleteMany() on a 403 sent %d requests, want 1", rt.calls)
	}
}

func TestS3Store_DeleteManyEscapesKeysInTheXMLBody(t *testing.T) {
	keys := []string{`a&b<c>"d'.txt`, "tenants/x/ü 1.pdf"}
	rt := xmlRT(http.StatusOK, xmlHeader+`<DeleteResult/>`)
	if err := newWireStore(t, rt).DeleteMany(context.Background(), keys); err != nil {
		t.Fatalf("DeleteMany() returned unexpected error: %v", err)
	}
	var body struct {
		Keys []string `xml:"Object>Key"`
	}
	if err := xml.Unmarshal(rt.lastBody, &body); err != nil {
		t.Fatalf("DeleteMany() body is not well-formed XML: %v: %q", err, rt.lastBody)
	}
	if len(body.Keys) != len(keys) || body.Keys[0] != keys[0] || body.Keys[1] != keys[1] {
		t.Errorf("DeleteMany() body keys = %q, want %q", body.Keys, keys)
	}
}

func TestS3Store_DeleteManyNamesTheFirstOfSeveralPerKeyErrors(t *testing.T) {
	rt := xmlRT(http.StatusOK, xmlHeader+`<DeleteResult>`+
		`<Error><Key>k2</Key><Code>AccessDenied</Code></Error>`+
		`<Error><Key>k3</Key><Code>InternalError</Code></Error></DeleteResult>`)
	err := newWireStore(t, rt).DeleteMany(context.Background(), []string{"k1", "k2", "k3"})
	if err == nil {
		t.Fatal("DeleteMany() returned nil on a partial failure")
	}
	for _, want := range []string{"k2", "AccessDenied", "2 of 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("DeleteMany() error = %v, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "k3") {
		t.Errorf("DeleteMany() error = %v, want only the first failed key", err)
	}
}

func TestS3Store_ListWithAnEmptyPrefixSendsNoPrefixAndStillSucceeds(t *testing.T) {
	rt := xmlRT(http.StatusOK, xmlHeader+`<ListBucketResult><Contents><Key>a</Key></Contents></ListBucketResult>`)
	keys, err := newWireStore(t, rt).List(context.Background(), "")
	if err != nil {
		t.Fatalf("List(\"\") returned unexpected error: %v", err)
	}
	if len(keys) != 1 || keys[0] != "a" {
		t.Errorf("List(\"\") = %v, want [a]", keys)
	}
	if rt.calls != 1 || rt.lastReq.URL.Query().Get("prefix") != "" {
		t.Errorf("List(\"\") sent %d requests with prefix %q, want 1 with none", rt.calls, rt.lastReq.URL.Query().Get("prefix"))
	}
}

func TestS3Store_ListKeysNeedingEscapingRoundTrip(t *testing.T) {
	rt := xmlRT(http.StatusOK, xmlHeader+`<ListBucketResult><Contents><Key>a&amp;b&lt;c.txt</Key></Contents></ListBucketResult>`)
	keys, err := newWireStore(t, rt).List(context.Background(), "p&q/ü ")
	if err != nil || len(keys) != 1 || keys[0] != "a&b<c.txt" {
		t.Fatalf("List() = %q, %v, want [a&b<c.txt]", keys, err)
	}
	if got := rt.lastReq.URL.Query().Get("prefix"); got != "p&q/ü " {
		t.Errorf("List() prefix on the wire = %q, want %q", got, "p&q/ü ")
	}
}

func TestS3Store_DeleteManySendsACRC32ChecksumNotContentMD5(t *testing.T) {
	rt := xmlRT(http.StatusOK, xmlHeader+`<DeleteResult/>`)
	if err := newWireStore(t, rt).DeleteMany(context.Background(), []string{"k1"}); err != nil {
		t.Fatalf("DeleteMany() returned unexpected error: %v", err)
	}
	if rt.lastReq.Header.Get("X-Amz-Checksum-Crc32") == "" || rt.lastReq.Header.Get("Content-MD5") != "" {
		t.Errorf("DeleteMany() headers = %v, want x-amz-checksum-crc32 and no Content-MD5", rt.lastReq.Header)
	}
}
