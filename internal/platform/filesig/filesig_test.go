package filesig_test

import (
	"bytes"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/filesig"
)

func TestMatches_SignatureTable(t *testing.T) {
	cases := []struct {
		format string
		in     string
		want   bool
	}{
		{"pdf", "%PDF-1.7", true},
		{"pdf", "%PDF", false},
		{"pdf", "", false},
		{"pdf", " %PDF-1.7", false},
		{"pdf", "\x89PNG\r\n\x1a\n", false},
		{"docx", "PK\x03\x04rest", true},
		{"xlsx", "PK\x03\x04rest", true},
		{"docx", "PK\x05\x06", false},
		{"docx", "PK\x03", false},
		{"csv", "anything", true},
		{"csv", "", true},
		{"png", "\x89PNG", false},
		{"", "%PDF-", false},
	}
	for _, c := range cases {
		if got := filesig.Matches(c.format, []byte(c.in)); got != c.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", c.format, c.in, got, c.want)
		}
	}
}

func TestMatches_ReadsOnlyTheHead(t *testing.T) {
	b := append([]byte("%PDF-"), bytes.Repeat([]byte{0}, 1<<20)...)
	if !filesig.Matches("pdf", b) {
		t.Error("Matches(pdf) = false for a %PDF- head followed by 1 MiB of zeros")
	}
}
