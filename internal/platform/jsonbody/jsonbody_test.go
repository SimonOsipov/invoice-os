package jsonbody

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type payload struct {
	A int `json:"a"`
}

func decode(t *testing.T, body io.Reader, limit int64) (payload, error) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", body)
	var p payload
	err := Decode(httptest.NewRecorder(), r, limit, &p)
	return p, err
}

func padded(n int) string {
	return `{"a":1}` + strings.Repeat(" ", n-len(`{"a":1}`))
}

func TestDecode_OneValueOverTheLimitIsTooLarge(t *testing.T) {
	_, err := decode(t, strings.NewReader(`{"a":"`+strings.Repeat("x", 2048)), 1024)
	if !TooLarge(err) {
		t.Fatalf("TooLarge(%v) = false", err)
	}
}

func TestDecode_TrailingBytesOverTheLimitAreTooLarge(t *testing.T) {
	p, err := decode(t, strings.NewReader(`{"a":1}`+strings.Repeat(" ", 2048)), 1024)
	if !TooLarge(err) {
		t.Fatalf("TooLarge(%v) = false", err)
	}
	if p.A != 1 {
		t.Fatalf("A = %d, want 1", p.A)
	}
}

func TestDecode_ExactlyTheLimitDecodes(t *testing.T) {
	p, err := decode(t, strings.NewReader(padded(1024)), 1024)
	if err != nil || p.A != 1 {
		t.Fatalf("err = %v, A = %d", err, p.A)
	}
}

func TestDecode_OneByteOverTheLimitIsTooLarge(t *testing.T) {
	_, err := decode(t, strings.NewReader(padded(1025)), 1024)
	if !TooLarge(err) {
		t.Fatalf("TooLarge(%v) = false", err)
	}
}

func TestDecode_MalformedUnderTheLimitIsNotTooLarge(t *testing.T) {
	_, err := decode(t, strings.NewReader(`{"a":`), 1024)
	if err == nil || TooLarge(err) {
		t.Fatalf("err = %v, want non-nil and not too large", err)
	}
}

func TestDecode_EmptyBodyIsEOF(t *testing.T) {
	_, err := decode(t, strings.NewReader(""), 1024)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestDecode_TrailingBytesWithinTheLimitAreIgnored(t *testing.T) {
	p, err := decode(t, strings.NewReader(`{"a":1} junk`), 1024)
	if err != nil || p.A != 1 {
		t.Fatalf("err = %v, A = %d", err, p.A)
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestDecode_ReadsAtMostOneByteOverTheLimit(t *testing.T) {
	cr := &countingReader{r: strings.NewReader(`{"a":"` + strings.Repeat("x", 1<<20))}
	_, err := decode(t, cr, 1024)
	if !TooLarge(err) {
		t.Fatalf("TooLarge(%v) = false", err)
	}
	if cr.n > 1025 {
		t.Fatalf("read %d bytes, want <= 1025", cr.n)
	}
}

func TestTooLarge_FalseForNilAndForeignErrors(t *testing.T) {
	if TooLarge(nil) || TooLarge(io.EOF) {
		t.Fatal("TooLarge true for nil or io.EOF")
	}
}

func TestLimits_AreTheStatedValues(t *testing.T) {
	if DefaultLimit != 1<<20 || LineItemsLimit != 4<<20 {
		t.Fatalf("DefaultLimit = %d, LineItemsLimit = %d", DefaultLimit, LineItemsLimit)
	}
}
