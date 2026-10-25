package document_test

import (
	"encoding/xml"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

// memS3 is an in-memory S3 over an http.RoundTripper: PutObject, ListObjectsV2
// and DeleteObjects against a bucket-in-host endpoint.
type memS3 struct {
	mu         sync.Mutex
	objects    map[string][]byte
	puts       int
	lists      int
	deletes    int
	deleteKeys []int // key count of each DeleteObjects request
	failKey    string
	dropDelete bool
}

func newMemS3(t *testing.T) (*memS3, document.ObjectStore) {
	t.Helper()
	m := &memS3{objects: map[string][]byte{}}
	store, err := document.NewS3Store(document.Config{
		Bucket:          wireBucket,
		Endpoint:        wireEndpoint,
		Region:          "auto",
		AccessKeyID:     "tid_test_access_key",
		SecretAccessKey: "tsec_test_secret_key",
	}, &http.Client{Transport: m})
	if err != nil {
		t.Fatalf("NewS3Store() returned unexpected error: %v", err)
	}
	return m, store
}

func (m *memS3) requests() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.puts + m.lists + m.deletes
}

func (m *memS3) keysUnder(prefix string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sortedKeys(prefix)
}

func (m *memS3) sortedKeys(prefix string) []string {
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func (m *memS3) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	q := req.URL.Query()
	switch {
	case req.Method == http.MethodPut:
		m.puts++
		m.objects[strings.TrimPrefix(req.URL.Path, "/")] = body
		return xmlResp(req, http.StatusOK, ""), nil
	case req.Method == http.MethodGet && q.Get("list-type") == "2":
		m.lists++
		max, _ := strconv.Atoi(q.Get("max-keys"))
		keys := m.sortedKeys(q.Get("prefix"))
		if max > 0 && len(keys) > max {
			keys = keys[:max]
		}
		var b strings.Builder
		b.WriteString(`<ListBucketResult><IsTruncated>false</IsTruncated>`)
		for _, k := range keys {
			b.WriteString("<Contents><Key>")
			_ = xml.EscapeText(&b, []byte(k))
			b.WriteString("</Key></Contents>")
		}
		b.WriteString(`</ListBucketResult>`)
		return xmlResp(req, http.StatusOK, b.String()), nil
	case req.Method == http.MethodPost && req.URL.Query().Has("delete"):
		m.deletes++
		var in struct {
			Keys []string `xml:"Object>Key"`
		}
		if err := xml.Unmarshal(body, &in); err != nil {
			return xmlResp(req, http.StatusBadRequest, ""), nil
		}
		m.deleteKeys = append(m.deleteKeys, len(in.Keys))
		var b strings.Builder
		b.WriteString(`<DeleteResult>`)
		for _, k := range in.Keys {
			if k == m.failKey {
				b.WriteString("<Error><Key>")
				_ = xml.EscapeText(&b, []byte(k))
				b.WriteString("</Key><Code>AccessDenied</Code></Error>")
				continue
			}
			if !m.dropDelete {
				delete(m.objects, k)
			}
		}
		b.WriteString(`</DeleteResult>`)
		return xmlResp(req, http.StatusOK, b.String()), nil
	}
	return xmlResp(req, http.StatusNotImplemented, ""), nil
}

func xmlResp(req *http.Request, status int, body string) *http.Response {
	return newFakeResponse(req, status, http.Header{"Content-Type": {"application/xml"}}, xmlHeader+body)
}
