package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type needle struct {
	kind   string
	digits int
	digest string // hex SHA-256 of the decimal string
}

type finding struct {
	path string
	line int
	kind string
}

// pinned holds digests only: the guard must not contain the values it forbids.
var pinned = []needle{
	{"GCP prod project number", 12, "b7343bb721f376f80356cb2c71b30071c1d7ac138f5074d7523f03ea025e73df"},
	{"HubSpot portal id", 9, "a884363b5d6a9f30a90516f54e984f9138d0cced9e9f5ad323975123302b4697"},
}

// envOf maps a needle kind to the Actions variable that holds its value.
var envOf = map[string]string{
	"GCP prod project number": "GCP_WIF_PROVIDER",
	"HubSpot portal id":       "HUBSPOT_PORTAL_ID",
}

var providerProject = regexp.MustCompile(`^projects/([0-9]+)/`)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// scan hashes every needle-length window of each ASCII digit run, so an id inside a longer run is found.
func scan(path string, data []byte, needles []needle) []finding {
	var out []finding
	line := 1
	for i := 0; i < len(data); {
		if !isDigit(data[i]) {
			if data[i] == '\n' {
				line++
			}
			i++
			continue
		}
		j := i
		for j < len(data) && isDigit(data[j]) {
			j++
		}
		for _, n := range needles {
			for k := i; k+n.digits <= j; k++ {
				if sum(string(data[k:k+n.digits])) == n.digest {
					out = append(out, finding{path, line, n.kind})
				}
			}
		}
		i = j
	}
	return out
}

func run(dir string, needles []needle, stdout, stderr io.Writer) int {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = dir
	list, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(stderr, "prodidgate: git ls-files: %v\n", err)
		return 2
	}
	var found []finding
	read := 0
	for _, p := range bytes.Split(list, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		path := string(p)
		full := filepath.Join(dir, path)
		info, err := os.Lstat(full)
		if err != nil {
			fmt.Fprintf(stderr, "prodidgate: %s: %v\n", path, err)
			return 2
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			fmt.Fprintf(stderr, "prodidgate: %s: %v\n", path, err)
			return 2
		}
		read++
		found = append(found, scan(path, data, needles)...)
	}
	for _, f := range found {
		fmt.Fprintf(stdout, "::error file=%s,line=%d::%s contains the %s\n", f.path, f.line, f.path, f.kind)
	}
	if len(found) > 0 {
		return 1
	}
	fmt.Fprintf(stdout, "prodidgate: no production id in %d tracked files\n", read)
	return 0
}

// selfcheck proves each pinned digest matches its Actions variable; a wrong digest would blind the scan.
func selfcheck(getenv func(string) string, needles []needle, stdout, stderr io.Writer) int {
	code := 0
	for _, n := range needles {
		name := envOf[n.kind]
		val := strings.TrimSpace(getenv(name))
		if val == "" {
			fmt.Fprintf(stdout, "::notice::%s is not set; self-check skipped\n", name)
			continue
		}
		if name == "GCP_WIF_PROVIDER" {
			m := providerProject.FindStringSubmatch(val)
			if m == nil {
				fmt.Fprintf(stderr, "prodidgate: %s holds no projects/<digits>/\n", name)
				code = 1
				continue
			}
			val = m[1]
		}
		if sum(val) != n.digest {
			fmt.Fprintf(stderr, "prodidgate: the pinned digest for the %s does not match %s\n", n.kind, name)
			code = 1
		}
	}
	if code == 0 {
		fmt.Fprintln(stdout, "prodidgate: pinned digests match the variables that are set")
	}
	return code
}
