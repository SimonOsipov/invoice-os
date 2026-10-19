package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var manifests = []string{"go.mod", "sidecar/docling/requirements.txt", "pnpm-lock.yaml"}

const maxIgnoreDays = 30

// Finding is one (package, group) pair that counts.
type Finding struct {
	IDs      []string
	Package  string
	Version  string
	Manifest string
	Why      string
}

func (f Finding) String() string {
	return fmt.Sprintf("counted: %s %s@%s (%s) %s", strings.Join(f.IDs, "/"), f.Package, f.Version, f.Manifest, f.Why)
}

type osvDoc struct {
	Results []struct {
		Source   struct{ Path string }
		Packages []struct {
			Package struct{ Name, Version string }
			Groups  []struct {
				IDs         []string
				Aliases     []string
				MaxSeverity string `json:"max_severity"`
			}
			Vulnerabilities []struct {
				ID               string
				DatabaseSpecific struct{ Severity string } `json:"database_specific"`
			}
		}
	}
}

// Counted applies the severity rule to one osv-scanner JSON document.
// lookup rates a GHSA id the document has no record of ("" = no rating).
func Counted(raw []byte, lookup func(ghsa string) (string, error)) (found []Finding, total int, err error) {
	var d osvDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, 0, fmt.Errorf("osv output: %w", err)
	}
	for _, r := range d.Results {
		for _, p := range r.Packages {
			recs := map[string]string{}
			for _, v := range p.Vulnerabilities {
				recs[v.ID] = v.DatabaseSpecific.Severity
			}
			for _, g := range p.Groups {
				total++
				why, counts, err := rate(g.IDs, g.Aliases, g.MaxSeverity, recs, lookup)
				if err != nil {
					return nil, 0, fmt.Errorf("%s %s@%s: %w", strings.Join(g.IDs, "/"), p.Package.Name, p.Package.Version, err)
				}
				if counts {
					found = append(found, Finding{g.IDs, p.Package.Name, p.Package.Version, r.Source.Path, why})
				}
			}
		}
	}
	return found, total, nil
}

// rate: a GHSA rating (record in the output, else lookup) decides; CVSS only when none exists.
func rate(ids, aliases []string, maxSev string, recs map[string]string, lookup func(string) (string, error)) (string, bool, error) {
	seen := map[string]bool{}
	var rated []string
	for _, id := range append(append([]string{}, ids...), aliases...) {
		if !strings.HasPrefix(id, "GHSA-") || seen[id] {
			continue
		}
		seen[id] = true
		sev, ok := recs[id]
		if !ok {
			var err error
			if sev, err = lookup(id); err != nil {
				return "", false, err
			}
		}
		if sev != "" {
			rated = append(rated, strings.ToUpper(sev))
		}
	}
	if len(rated) > 0 {
		for _, s := range rated {
			if s == "HIGH" || s == "CRITICAL" {
				return "GHSA " + s, true, nil
			}
		}
		return "", false, nil
	}
	if maxSev == "" {
		return "", false, nil
	}
	score, err := strconv.ParseFloat(maxSev, 64)
	if err != nil {
		return "", false, fmt.Errorf("max_severity %q: %w", maxSev, err)
	}
	return "CVSS " + maxSev, score >= 7.0, nil
}

type ignoreEntry struct {
	line             int
	id, reason       string
	until            time.Time
	hasID, hasReason bool
	hasUntil         bool
	untilLine        int
	seenKeys         map[string]bool
}

// IgnoreProblems checks every osv-scanner.toml under root against today (UTC).
func IgnoreProblems(root string, today time.Time) ([]string, error) {
	var problems []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".claude", ".ralph":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Name() != "osv-scanner.toml" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		problems = append(problems, checkIgnoreFile(path, string(raw), today)...)
		return nil
	})
	return problems, err
}

// ceiling: TOML subset (single-line strings, dates); anything else fails closed
func checkIgnoreFile(path, text string, today time.Time) []string {
	var problems []string
	bad := func(line int, format string, a ...any) {
		problems = append(problems, fmt.Sprintf("%s:%d: %s", path, line, fmt.Sprintf(format, a...)))
	}
	limit := dateOf(today).AddDate(0, 0, maxIgnoreDays)
	var cur *ignoreEntry
	finish := func() {
		if cur == nil {
			return
		}
		if !cur.hasID || cur.id == "" {
			bad(cur.line, "ignore entry has no id")
		}
		if !cur.hasReason || strings.TrimSpace(cur.reason) == "" {
			bad(cur.line, "ignore entry has no reason")
		}
		if !cur.hasUntil {
			bad(cur.line, "ignore entry has no ignoreUntil (osv-scanner would ignore it forever)")
		} else if dateOf(cur.until).After(limit) {
			bad(cur.untilLine, "ignoreUntil %s is more than %d days after %s", dateOf(cur.until).Format("2006-01-02"), maxIgnoreDays, dateOf(today).Format("2006-01-02"))
		}
		cur = nil
	}
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			finish()
			if line != "[[IgnoredVulns]]" {
				bad(n, "unsupported table %s (only [[IgnoredVulns]])", line)
				continue
			}
			cur = &ignoreEntry{line: n, seenKeys: map[string]bool{}}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !ok {
			bad(n, "unsupported line %q", line)
			continue
		}
		if cur == nil {
			bad(n, "key %q outside an [[IgnoredVulns]] table", key)
			continue
		}
		if key != "id" && key != "reason" && key != "ignoreUntil" {
			bad(n, "unsupported key %q", key)
			continue
		}
		if cur.seenKeys[key] {
			bad(n, "duplicate key %q", key)
			continue
		}
		cur.seenKeys[key] = true
		if key == "ignoreUntil" {
			t, err := parseTOMLDate(val)
			if err != nil {
				bad(n, "ignoreUntil: %v", err)
				continue
			}
			cur.hasUntil, cur.until, cur.untilLine = true, t, n
			continue
		}
		s, err := parseTOMLString(val)
		if err != nil {
			bad(n, "%s: %v", key, err)
			continue
		}
		if key == "id" {
			cur.hasID, cur.id = true, s
		} else {
			cur.hasReason, cur.reason = true, s
		}
	}
	finish()
	return problems
}

func dateOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// parseTOMLString reads one single-line basic string, then an optional comment.
func parseTOMLString(v string) (string, error) {
	if !strings.HasPrefix(v, `"`) || strings.HasPrefix(v, `"""`) {
		return "", errors.New("want a single-line basic string")
	}
	end := -1
	for i := 1; i < len(v); i++ {
		if v[i] == '\\' {
			i++
		} else if v[i] == '"' {
			end = i
			break
		}
	}
	if end < 0 {
		return "", errors.New("unterminated string")
	}
	if rest := strings.TrimSpace(v[end+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
		return "", errors.New("text after the string")
	}
	s, err := strconv.Unquote(v[:end+1])
	if err != nil {
		return "", fmt.Errorf("unsupported string escape: %w", err)
	}
	return s, nil
}

// parseTOMLDate reads an unquoted date or RFC 3339 datetime, then an optional comment.
func parseTOMLDate(v string) (time.Time, error) {
	tok := v
	if i := strings.IndexAny(v, " \t#"); i >= 0 {
		tok = v[:i]
		if rest := strings.TrimSpace(v[i:]); rest != "" && !strings.HasPrefix(rest, "#") {
			return time.Time{}, errors.New("want an unquoted date or RFC 3339 datetime")
		}
	}
	if t, err := time.Parse("2006-01-02", tok); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, tok); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("want an unquoted date or RFC 3339 datetime")
}

// Fixed scanner arguments; no --config, so osv-scanner reads osv-scanner.toml next to the manifest.
func scannerArgs(out string) []string {
	args := []string{"scan", "source", "--no-resolve", "--all-vulns", "--no-call-analysis=go", "--format", "json", "--output-file", out}
	for _, m := range manifests {
		args = append(args, "-L", m)
	}
	return args
}

var scannedLine = regexp.MustCompile(`(?m)^Scanned (.+) file and found (\d+) packages?\r?$`)

// missingManifest returns the first manifest the scanner log does not show as scanned with N > 0.
func missingManifest(log string) string {
	for _, m := range manifests {
		ok := false
		for _, sub := range scannedLine.FindAllStringSubmatch(log, -1) {
			if n, _ := strconv.Atoi(sub[2]); n > 0 && (sub[1] == m || strings.HasSuffix(sub[1], "/"+m)) {
				ok = true
			}
		}
		if !ok {
			return m
		}
	}
	return ""
}

// Run returns the exit code: 0 clean, 1 counted finding or bad ignore entry, 2 tool error.
// The scanner argv is fixed here; osvAPI is the base URL of the OSV API.
func Run(osv, root, osvAPI string, today time.Time, stdout, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "advisorygate: "+format+"\n", a...)
		return 2
	}
	code := 0
	problems, err := IgnoreProblems(root, today)
	if err != nil {
		return fail("ignore files: %v", err)
	}
	for _, p := range problems {
		fmt.Fprintln(stdout, "ignore:", p)
		code = 1
	}

	osvAbs, err := filepath.Abs(osv)
	if err != nil {
		return fail("%v", err)
	}
	tmp, err := os.MkdirTemp("", "advisorygate")
	if err != nil {
		return fail("%v", err)
	}
	defer os.RemoveAll(tmp)
	outFile := filepath.Join(tmp, "osv.json")

	var log bytes.Buffer
	cmd := exec.Command(osvAbs, scannerArgs(outFile)...)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = &log, &log
	runErr := cmd.Run()
	stdout.Write(log.Bytes())
	exit := 0
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			return fail("run scanner: %v", runErr)
		}
		exit = ee.ExitCode()
	}
	if exit != 0 && exit != 1 {
		return fail("scanner exited %d (tool error, not a scan result)", exit)
	}
	raw, err := os.ReadFile(outFile)
	if err != nil {
		return fail("scanner wrote no output file: %v", err)
	}
	if m := missingManifest(log.String()); m != "" {
		return fail("scanner log shows no packages scanned for %s", m)
	}

	found, total, err := Counted(raw, newLookup(osvAPI))
	if err != nil {
		return fail("%v", err)
	}
	for _, f := range found {
		if rel, err := filepath.Rel(root, f.Manifest); err == nil && !strings.HasPrefix(rel, "..") {
			f.Manifest = rel
		}
		fmt.Fprintln(stdout, f)
		code = 1
	}
	fmt.Fprintf(stdout, "advisories: %d high or critical findings of %d total\n", len(found), total)
	return code
}

// newLookup rates a GHSA id through the OSV API, once per id.
func newLookup(base string) func(string) (string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	cache := map[string]string{}
	return func(id string) (string, error) {
		if sev, ok := cache[id]; ok {
			return sev, nil
		}
		resp, err := client.Get(strings.TrimRight(base, "/") + "/v1/vulns/" + url.PathEscape(id))
		if err != nil {
			return "", fmt.Errorf("lookup %s: %w", id, err)
		}
		defer resp.Body.Close()
		sev := ""
		switch resp.StatusCode {
		case http.StatusOK:
			var v struct {
				DatabaseSpecific struct{ Severity string } `json:"database_specific"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
				return "", fmt.Errorf("lookup %s: %w", id, err)
			}
			sev = strings.ToUpper(v.DatabaseSpecific.Severity)
		case http.StatusNotFound:
		default:
			return "", fmt.Errorf("lookup %s: HTTP %d", id, resp.StatusCode)
		}
		cache[id] = sev
		return sev, nil
	}
}
