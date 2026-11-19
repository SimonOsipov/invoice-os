package platform

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// CaptureStdout points os.Stdout at a pipe; call the returned func once to restore it and read
// what the process wrote. It is exported so the external tests share the one implementation.
func CaptureStdout(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	t.Cleanup(func() {
		os.Stdout = prev
		_ = w.Close()
		_ = r.Close()
	})
	return func() string {
		os.Stdout = prev
		_ = w.Close()
		<-done
		return buf.String()
	}
}
