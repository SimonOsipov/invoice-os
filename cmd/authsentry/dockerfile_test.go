package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthImage_RunsGoTrueUnderTheWrapper(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "sidecar", "auth", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	var final []string
	for _, l := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(strings.ToUpper(l), "FROM ") {
			final = nil
		}
		final = append(final, l)
	}
	if !strings.Contains(final[0], "/supabase/auth:") {
		t.Fatalf("final stage starts %q, want the supabase/auth FROM", final[0])
	}
	for _, want := range []string{
		`COPY --from=wrap /out/authsentry /usr/local/bin/authsentry`,
		`ENTRYPOINT ["/usr/local/bin/authsentry"]`,
		`CMD ["auth"]`,
	} {
		found := false
		for _, l := range final {
			found = found || l == want
		}
		if !found {
			t.Errorf("final stage lacks %q", want)
		}
	}
}
