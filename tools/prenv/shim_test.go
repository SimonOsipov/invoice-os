package main

import (
	"os"
	"runtime"
	"testing"
)

var shimTrampoline string

// writeShim: a fresh executable costs ~0.8 s on its first exec on macOS; symlink one warmed trampoline instead.
func writeShim(t *testing.T, path, body string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(path+".body", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(shimTrampoline, path); err != nil {
		t.Fatal(err)
	}
}
