// docling_internal_test.go: the one Client.Timeout pin T-05-15's 20s sleep alone cannot catch
// (a 30s fixed timeout would ship silently). Internal because client is unexported.
package extraction

import "testing"

func TestNewDoclingReader_SetsNoClientTimeout(t *testing.T) {
	r, err := NewDoclingReader("http://127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewDoclingReader: %v", err)
	}
	if r.client.Timeout != 0 {
		t.Errorf("client.Timeout = %v, want 0: a fixed timeout would truncate a real cold start", r.client.Timeout)
	}
}
