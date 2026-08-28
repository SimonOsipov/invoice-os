// pdfium_build_test.go: AC-1's two build guards. go-pdfium is pinned, and CI proves the
// module still builds with cgo off -- the shared Dockerfile compiles every service
// CGO_ENABLED=0, so a cgo backend strands the whole fleet, not just this service.
//
// Both scans carry a floor and a control needle, per reachability_test.go:1-11: a scan that
// reached nothing reports exactly like a clean repo.
//
// Stdlib only. deps_test.go scan B walks test imports too, and any in-module import outside
// internal/platform/* fails it.
package extraction_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	pdfiumModule  = "github.com/klippa-app/go-pdfium"
	pdfiumVersion = "v1.19.8"

	// Control needle for the go.mod parse: a require line that is present today.
	pbGoModNeedle = "github.com/riverqueue/river"

	// Floors, measured on this tree: go.mod holds 102 require lines, ci.yml 80 steps.
	pbMinRequires = 50
	pbMinCISteps  = 40
)

// pbRequires parses go.mod into module -> version. Handles both the require ( ... ) block
// and a single-line require, and drops the // indirect marker.
func pbRequires(t *testing.T, path string) map[string]string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	out := map[string]string{}
	inBlock := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "":
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case line == "require (":
			inBlock = true
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimPrefix(line, "require ")
		case !inBlock:
			continue
		}
		if mod, ver, ok := strings.Cut(line, " "); ok {
			out[mod] = strings.TrimSpace(ver)
		}
	}
	return out
}

func TestPDFium_ModuleVersionIsPinned(t *testing.T) {
	root := rxRepoRoot(t)
	gomod := filepath.Join(root, "go.mod")

	reqs := pbRequires(t, gomod)
	if len(reqs) < pbMinRequires {
		t.Fatalf("parsed %d require line(s) from %s, want at least %d -- a missing-module report over a broken parse means nothing", len(reqs), gomod, pbMinRequires)
	}
	if reqs[pbGoModNeedle] == "" {
		t.Fatalf("the parse did not find %s in %s; it can no longer find a require line that is certainly there, so the verdict below means nothing", pbGoModNeedle, gomod)
	}

	got, ok := reqs[pdfiumModule]
	if !ok {
		t.Errorf("%s does not require %s: AC-1 pins the PDF reader, and the wazero backend is what keeps the build CGO_ENABLED=0", gomod, pdfiumModule)
		return
	}
	if got != pdfiumVersion {
		t.Errorf("%s requires %s %s, want %s: the version is pinned so a golden corpus stays comparable across runs", gomod, pdfiumModule, got, pdfiumVersion)
	}
}

// ciStep is one entry of a workflow steps: list. run holds the command lines; env holds the
// step-level env mapping.
type ciStep struct {
	name string
	line int
	env  map[string]string
	run  []string
}

// pbStepKey reads one step-level key and sets the collector mode for the lines below it.
func pbStepKey(s *ciStep, body string, mode *string) {
	k, v, ok := strings.Cut(body, ":")
	if !ok {
		*mode = ""
		return
	}
	k, v = strings.TrimSpace(k), strings.TrimSpace(v)
	switch k {
	case "name":
		s.name = strings.Trim(v, `"`)
		*mode = ""
	case "run":
		*mode = "run"
		// An inline run: carries its command here; a block scalar (| or >, with any
		// chomping suffix) carries it on the lines below.
		if v != "" && !strings.HasPrefix(v, "|") && !strings.HasPrefix(v, ">") {
			s.run = append(s.run, v)
		}
	case "env":
		*mode = "env"
	default:
		*mode = ""
	}
}

// pbCISteps parses a workflow into its steps. Indentation-driven: a steps: key fixes the
// list-marker column, and everything more indented belongs to the step above.
func pbCISteps(t *testing.T, path string) []ciStep {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var steps []ciStep
	var cur *ciStep
	stepIndent, mode := -1, ""

	flush := func() {
		if cur != nil {
			steps = append(steps, *cur)
			cur = nil
		}
		mode = ""
	}

	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, " \t\r")
		body := strings.TrimLeft(line, " ")
		if body == "" || strings.HasPrefix(body, "#") {
			continue // YAML comments never contribute a command
		}
		indent := len(line) - len(body)

		if body == "steps:" {
			flush()
			stepIndent = indent + 2
			continue
		}
		if stepIndent < 0 {
			continue
		}
		switch {
		case indent == stepIndent && strings.HasPrefix(body, "- "):
			flush()
			cur = &ciStep{line: i + 1, env: map[string]string{}}
			pbStepKey(cur, strings.TrimSpace(body[2:]), &mode)
		case indent < stepIndent:
			flush()
			stepIndent = -1
		case cur == nil:
			continue
		case indent == stepIndent+2:
			pbStepKey(cur, body, &mode)
		default: // deeper than a step key: the body of the block opened above
			switch mode {
			case "run":
				cur.run = append(cur.run, body)
			case "env":
				if k, v, ok := strings.Cut(body, ":"); ok {
					cur.env[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
		}
	}
	flush()
	return steps
}

// pbFindStep returns the one step with this name. Two steps are named Build (go and
// frontend), so callers needing a control needle must pick a unique name.
func pbFindStep(steps []ciStep, name string) (ciStep, bool) {
	for _, s := range steps {
		if s.name == name {
			return s, true
		}
	}
	return ciStep{}, false
}

func TestCI_BuildsWithCgoDisabled(t *testing.T) {
	root := rxRepoRoot(t)
	wf := filepath.Join(root, ".github", "workflows", "ci.yml")

	steps := pbCISteps(t, wf)
	if len(steps) < pbMinCISteps {
		t.Fatalf("parsed %d step(s) from %s, want at least %d -- a cgo verdict over a broken parse means nothing", len(steps), wf, pbMinCISteps)
	}

	// Control needle: Vet is unique, its command is known, and it must NOT carry the
	// neighbouring step's command -- a splitter that merged the list would see both.
	vet, ok := pbFindStep(steps, "Vet")
	if !ok {
		t.Fatalf("the parse found no step named Vet in %s; it can no longer find a step that is certainly there, so the verdict below means nothing", wf)
	}
	joined := strings.Join(vet.run, "\n")
	if !strings.Contains(joined, "go vet ./...") {
		t.Fatalf("the Vet step parsed with no go vet command; run bodies are not being read, so the verdict below means nothing")
	}
	if strings.Contains(joined, "gofmt -l") {
		t.Fatalf("the Vet step carries the Format step's command; the steps are being merged, so the verdict below means nothing")
	}

	var builders []string
	cgoOff := false
	for _, s := range steps {
		for i, cmd := range s.run {
			if !strings.Contains(cmd, "go build ./...") {
				continue
			}
			builders = append(builders, fmt.Sprintf("%q (%s:%d)", s.name, filepath.Base(wf), s.line))
			// In scope means the step env sets it, or a line at or above the build
			// command does -- an inline prefix or an earlier export both count.
			if s.env["CGO_ENABLED"] == "0" {
				cgoOff = true
				continue
			}
			for _, before := range s.run[:i+1] {
				if strings.Contains(before, "CGO_ENABLED=0") {
					cgoOff = true
				}
			}
		}
	}
	if len(builders) == 0 {
		t.Fatalf("no step in %s runs go build ./...; the cgo verdict below would be vacuous", wf)
	}
	if !cgoOff {
		t.Errorf("no step in %s runs go build ./... with CGO_ENABLED=0 in scope (found: %s); the shared Dockerfile builds every service with cgo off, so without this step CI cannot detect a cgo backend at all and the failure lands on the fleet's image build", wf, strings.Join(builders, ", "))
	}
}
