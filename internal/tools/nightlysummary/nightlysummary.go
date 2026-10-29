package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	modulePrefix = "github.com/SimonOsipov/invoice-os"
	ciFile       = ".github/workflows/ci.yml"
	maxMessage   = 300
)

type suiteSpec struct {
	key, name string
	results   bool
}

type step struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	Number     int    `json:"number"`
}

type job struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	Steps      []step `json:"steps"`
}

type failure struct {
	Suite   string `json:"suite"`
	Test    string `json:"test"`
	File    string `json:"file"`
	Message string `json:"message"`
}

type flake struct {
	Suite string `json:"suite"`
	Test  string `json:"test"`
	File  string `json:"file"`
}

type suiteResult struct {
	Name   string `json:"name"`
	Passed int    `json:"passed"`
	Failed int    `json:"failed"`
	Flaky  int    `json:"flaky"`
}

type section struct{ name, path string }

type input struct {
	suites            []suiteSpec
	jobs              []job
	resultsDir        string
	workspace         string
	sections          []section
	runID             int64
	sha               string
	started, finished time.Time
}

type summary struct {
	runID             int64
	sha               string
	started, finished time.Time
	verdict           string
	suites            []suiteResult
	failures          []failure
	flakes            []flake
	sectionNames      []string
	sectionBodies     map[string]json.RawMessage
}

var reservedKeys = map[string]bool{
	"schema": true, "run_id": true, "sha": true, "started_at": true, "finished_at": true,
	"verdict": true, "suites": true, "failures": true, "flakes": true,
}

func parseSuites(text string) ([]suiteSpec, error) {
	var out []suiteSpec
	seen := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "=")
		if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("line %d: want key=Display name[=results]", i+1)
		}
		if len(parts) == 3 && parts[2] != "results" {
			return nil, fmt.Errorf("line %d: third field must be \"results\"", i+1)
		}
		if seen[parts[0]] {
			return nil, fmt.Errorf("line %d: duplicate key %q", i+1, parts[0])
		}
		seen[parts[0]] = true
		out = append(out, suiteSpec{key: parts[0], name: parts[1], results: len(parts) == 3})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no suites")
	}
	return out, nil
}

func parseJobs(raw []byte) ([]job, error) {
	var jobs []job
	if err := json.Unmarshal(raw, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

func parseSectionFlags(vals []string) ([]section, error) {
	var out []section
	seen := map[string]bool{}
	for _, v := range vals {
		name, path, ok := strings.Cut(v, "=")
		if !ok || name == "" || path == "" {
			return nil, fmt.Errorf("%q: want name=file", v)
		}
		if reservedKeys[name] || seen[name] {
			return nil, fmt.Errorf("%q: reserved or repeated name", name)
		}
		seen[name] = true
		out = append(out, section{name, path})
	}
	return out, nil
}

func clip(s string) string {
	if r := []rune(s); len(r) > maxMessage {
		return string(r[:maxMessage])
	}
	return s
}

// firstLine is the first non-blank line, trimmed and clipped.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return clip(l)
		}
	}
	return ""
}

type status int

const (
	statusPass status = iota
	statusFail
	statusFlake
)

// attempt is one run of one test; attempts sharing set+key are judged together.
type attempt struct {
	set, key, test, file, msg string
	pkg                       string // Go package; empty for JUnit
	isPkg                     bool
	failed                    bool
}

type outcome struct {
	set, pkg, test, file, msg string
	isPkg                     bool
	status                    status
}

// parsedFile is one result file; seen counts every test result in it, skips included.
type parsedFile struct {
	attempts  []attempt
	direct    []outcome
	seen      int
	truncated bool // a Go stream whose last line is not an event
}

// classify judges the attempts of each set+key: all failed is a failure, some failed a flake.
func classify(attempts []attempt) []outcome {
	type gk struct{ set, key string }
	idx := map[gk]int{}
	var groups [][]attempt
	for _, a := range attempts {
		k := gk{a.set, a.key}
		i, ok := idx[k]
		if !ok {
			i = len(groups)
			idx[k] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], a)
	}
	out := make([]outcome, 0, len(groups))
	for _, g := range groups {
		first := g[0]
		o := outcome{set: first.set, pkg: first.pkg, test: first.test, file: first.file, isPkg: first.isPkg}
		var failedAttempts []attempt
		for _, a := range g {
			if a.failed {
				failedAttempts = append(failedAttempts, a)
			}
		}
		switch {
		case len(failedAttempts) == 0:
			o.status = statusPass
		case len(failedAttempts) == len(g):
			o.status = statusFail
			o.msg = failedAttempts[len(failedAttempts)-1].msg
		default:
			o.status = statusFlake
			o.msg = failedAttempts[0].msg
		}
		out = append(out, o)
	}
	return out
}

// hideGoParents drops package passes and any entry a failed or flaky descendant already explains (D-02-15).
func hideGoParents(outs []outcome) []outcome {
	type pk struct{ set, pkg string }
	bad := map[pk][]string{}
	for _, o := range outs {
		if o.pkg != "" && !o.isPkg && o.status != statusPass {
			k := pk{o.set, o.pkg}
			bad[k] = append(bad[k], o.test)
		}
	}
	kept := outs[:0:0]
	for _, o := range outs {
		if o.pkg != "" {
			names := bad[pk{o.set, o.pkg}]
			if o.isPkg && (o.status == statusPass || len(names) > 0) {
				continue
			}
			if !o.isPkg && o.status != statusPass && hasDescendant(o.test, names) {
				continue
			}
		}
		kept = append(kept, o)
	}
	return kept
}

func hasDescendant(test string, names []string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, test+"/") {
			return true
		}
	}
	return false
}

// ---- Go ----

type goEvent struct {
	Action      string
	Package     string
	Test        string
	Output      string
	ImportPath  string
	FailedBuild string
}

type openTest struct {
	pkg, test string
	out       []string
	done      bool
}

type goParser struct {
	set      string
	open     map[string]*openTest
	opens    []*openTest
	pkgOut   map[string][]string
	buildOut map[string][]string
	pf       parsedFile
}

func goFile(pkg string) string {
	if pkg == modulePrefix {
		return "."
	}
	return strings.TrimPrefix(pkg, modulePrefix+"/")
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// goMessage is the first output line that is not a === or --- marker.
func goMessage(out []string) string {
	for _, chunk := range out {
		for _, l := range strings.Split(chunk, "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "=== ") || strings.HasPrefix(l, "--- ") {
				continue
			}
			return clip(l)
		}
	}
	return ""
}

func parseGo(r io.Reader, set string) (parsedFile, error) {
	p := &goParser{
		set: set, open: map[string]*openTest{},
		pkgOut: map[string][]string{}, buildOut: map[string][]string{},
	}
	br := bufio.NewReader(r)
	good, bad, lastBad := 0, 0, false
	for {
		line, err := br.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			var e goEvent
			if json.Unmarshal([]byte(s), &e) != nil {
				bad++
				lastBad = true
			} else {
				good++
				lastBad = false
				p.event(e)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return parsedFile{}, err
		}
	}
	if good == 0 && bad > 0 {
		return parsedFile{}, fmt.Errorf("no go test -json event in the file")
	}
	for _, o := range p.opens {
		p.finishUnfinished(o)
	}
	p.pf.truncated = lastBad
	return p.pf, nil
}

func (p *goParser) add(a attempt) {
	a.set = p.set
	p.pf.attempts = append(p.pf.attempts, a)
}

// finishUnfinished records a test that started and never reported (timeout, panic) as a failed attempt.
func (p *goParser) finishUnfinished(o *openTest) {
	if o.done {
		return
	}
	o.done = true
	msg := goMessage(o.out)
	if msg == "" {
		msg = "test did not finish"
	}
	p.pf.seen++
	p.add(attempt{key: o.pkg + "\x00" + o.test, test: o.test, file: goFile(o.pkg), pkg: o.pkg, failed: true, msg: msg})
}

func (p *goParser) event(e goEvent) {
	switch {
	case e.Action == "build-output":
		k := firstField(e.ImportPath)
		p.buildOut[k] = append(p.buildOut[k], e.Output)
	case e.Test != "":
		p.testEvent(e)
	case e.Package != "":
		p.packageEvent(e)
	}
}

func (p *goParser) testEvent(e goEvent) {
	k := e.Package + "\x00" + e.Test
	o := p.open[k]
	switch e.Action {
	case "run":
		o = &openTest{pkg: e.Package, test: e.Test}
		p.open[k] = o
		p.opens = append(p.opens, o)
	case "output":
		if o != nil {
			o.out = append(o.out, e.Output)
		}
	case "pass", "fail", "skip":
		if o == nil {
			o = &openTest{pkg: e.Package, test: e.Test}
		}
		o.done = true
		delete(p.open, k)
		p.pf.seen++
		if e.Action == "skip" {
			return
		}
		a := attempt{key: k, test: e.Test, file: goFile(e.Package), pkg: e.Package}
		if e.Action == "fail" {
			a.failed = true
			if a.msg = goMessage(o.out); a.msg == "" {
				a.msg = "test failed"
			}
		}
		p.add(a)
	}
}

func (p *goParser) packageEvent(e goEvent) {
	switch e.Action {
	case "start":
		p.pkgOut[e.Package] = nil
	case "output":
		p.pkgOut[e.Package] = append(p.pkgOut[e.Package], e.Output)
	case "pass", "fail":
		out := p.pkgOut[e.Package]
		delete(p.pkgOut, e.Package)
		a := attempt{key: e.Package + " (package)", test: e.Package + " (package)", file: goFile(e.Package), pkg: e.Package, isPkg: true}
		if e.Action == "fail" {
			a.failed = true
			a.msg = p.packageMessage(e, out)
			p.pf.seen++
		}
		p.add(a)
	case "skip":
		delete(p.pkgOut, e.Package)
	}
}

// packageMessage prefers the first non-# build error line, else the package's first output line.
func (p *goParser) packageMessage(e goEvent, out []string) string {
	for _, k := range []string{firstField(e.FailedBuild), e.Package} {
		for _, chunk := range p.buildOut[k] {
			for _, l := range strings.Split(chunk, "\n") {
				if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
					return clip(l)
				}
			}
		}
	}
	for _, chunk := range out {
		if l := firstLine(chunk); l != "" {
			return l
		}
	}
	return "package failed"
}

// ---- vitest ----

type vitestReport struct {
	TestResults []struct {
		Name             string `json:"name"`
		Status           string `json:"status"`
		Message          string `json:"message"`
		AssertionResults []struct {
			FullName        string   `json:"fullName"`
			Title           string   `json:"title"`
			Status          string   `json:"status"`
			FailureMessages []string `json:"failureMessages"`
		} `json:"assertionResults"`
	} `json:"testResults"`
}

func parseVitest(data []byte, workspace string) (parsedFile, error) {
	var rep vitestReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return parsedFile{}, err
	}
	prefix := strings.TrimRight(workspace, "/") + "/"
	var pf parsedFile
	for _, tr := range rep.TestResults {
		file := tr.Name
		if workspace != "" {
			file = strings.TrimPrefix(file, prefix)
		}
		anyFailed := false
		for _, ar := range tr.AssertionResults {
			pf.seen++
			name := ar.FullName
			if name == "" {
				name = ar.Title
			}
			o := outcome{test: name, file: file}
			if len(ar.FailureMessages) > 0 {
				o.msg = firstLine(ar.FailureMessages[0])
			}
			switch ar.Status {
			case "passed":
				o.status = statusPass
				if len(ar.FailureMessages) > 0 {
					o.status = statusFlake
				}
			case "failed":
				o.status = statusFail
				anyFailed = true
				if o.msg == "" {
					o.msg = "test failed"
				}
			default:
				continue
			}
			pf.direct = append(pf.direct, o)
		}
		if tr.Status == "failed" && !anyFailed {
			pf.seen++
			msg := firstLine(tr.Message)
			if msg == "" {
				msg = "file failed"
			}
			pf.direct = append(pf.direct, outcome{test: "(file)", file: file, msg: msg, status: statusFail})
		}
	}
	return pf, nil
}

// ---- JUnit ----

type junitProblem struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

type junitCase struct {
	Classname string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	File      string        `xml:"file,attr"`
	Failure   *junitProblem `xml:"failure"`
	Error     *junitProblem `xml:"error"`
	Skipped   *struct{}     `xml:"skipped"`
}

type junitSuite struct {
	Name   string       `xml:"name,attr"`
	Cases  []junitCase  `xml:"testcase"`
	Suites []junitSuite `xml:"testsuite"`
}

func parseJUnit(data []byte, set string) (parsedFile, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var roots []junitSuite
	for {
		tok, err := d.Token()
		if err != nil {
			return parsedFile{}, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "testsuites":
			var w struct {
				Suites []junitSuite `xml:"testsuite"`
			}
			if err := d.DecodeElement(&w, &start); err != nil {
				return parsedFile{}, err
			}
			roots = w.Suites
		case "testsuite":
			var s junitSuite
			if err := d.DecodeElement(&s, &start); err != nil {
				return parsedFile{}, err
			}
			roots = []junitSuite{s}
		default:
			return parsedFile{}, fmt.Errorf("unexpected root element <%s>", start.Name.Local)
		}
		break
	}
	var pf parsedFile
	var walk func(s junitSuite)
	walk = func(s junitSuite) {
		for _, c := range s.Cases {
			pf.seen++
			if c.Skipped != nil {
				continue
			}
			file := c.File
			if s.Name != "" && s.Name != "pytest" {
				file = strings.TrimSuffix(s.Name+"/"+c.File, "/")
			}
			test := c.Classname + "::" + c.Name
			a := attempt{set: set, key: c.File + "\x00" + test, test: test, file: file}
			if p := firstProblem(c); p != nil {
				a.failed = true
				if a.msg = firstLine(p.Message); a.msg == "" {
					a.msg = firstLine(p.Text)
				}
				if a.msg == "" {
					a.msg = "test failed"
				}
			}
			pf.attempts = append(pf.attempts, a)
		}
		for _, sub := range s.Suites {
			walk(sub)
		}
	}
	for _, s := range roots {
		walk(s)
	}
	return pf, nil
}

func firstProblem(c junitCase) *junitProblem {
	if c.Failure != nil {
		return c.Failure
	}
	return c.Error
}

// ---- one suite ----

// fileOrder puts junit-<n>.xml before junit-<n>-retry.xml, so attempts keep their order.
func fileOrder(names []string) {
	base := junitSet
	sort.SliceStable(names, func(i, j int) bool {
		bi, bj := base(names[i]), base(names[j])
		if bi != bj {
			return bi < bj
		}
		return !strings.HasSuffix(names[i], "-retry.xml") && strings.HasSuffix(names[j], "-retry.xml")
	})
}

func junitSet(name string) string {
	return strings.TrimSuffix(strings.TrimSuffix(name, ".xml"), "-retry")
}

type suiteFiles struct {
	outcomes   []outcome
	problems   []failure // unreadable and empty files
	seen       int
	unreadable bool
}

func readSuiteFiles(dir, key, workspace string) suiteFiles {
	var sf suiteFiles
	entries, err := os.ReadDir(dir)
	if err != nil {
		return sf
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && (strings.HasPrefix(n, "gotest-") && strings.HasSuffix(n, ".json") ||
			strings.HasPrefix(n, "vitest-") && strings.HasSuffix(n, ".json") ||
			strings.HasPrefix(n, "junit-") && strings.HasSuffix(n, ".xml")) {
			names = append(names, n)
		}
	}
	fileOrder(names)
	problem := func(name, test, msg string) {
		sf.problems = append(sf.problems, failure{Suite: key, Test: test, File: name, Message: msg})
	}
	var parsed []parsedFile
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			sf.unreadable = true
			problem(n, "results unreadable", firstLine(err.Error()))
			continue
		}
		var pf parsedFile
		if len(bytes.TrimSpace(data)) > 0 {
			switch {
			case strings.HasPrefix(n, "gotest-"):
				pf, err = parseGo(bytes.NewReader(data), n)
			case strings.HasPrefix(n, "vitest-"):
				pf, err = parseVitest(data, workspace)
			default:
				pf, err = parseJUnit(data, junitSet(n))
			}
		}
		if err != nil {
			sf.unreadable = true
			problem(n, "results unreadable", firstLine(err.Error()))
			continue
		}
		if pf.truncated {
			sf.unreadable = true
			problem(n, "results unreadable", "the go test -json stream ends mid-line")
		}
		if pf.seen == 0 {
			problem(n, "results empty", "the file holds no test results")
			continue
		}
		sf.seen += pf.seen
		parsed = append(parsed, pf)
	}
	sf.outcomes = judgeFiles(parsed)
	return sf
}

// judgeFiles classifies the attempts of all files, then appends the vitest verdicts.
func judgeFiles(pfs []parsedFile) []outcome {
	var attempts []attempt
	var direct []outcome
	for _, pf := range pfs {
		attempts = append(attempts, pf.attempts...)
		direct = append(direct, pf.direct...)
	}
	return append(hideGoParents(classify(attempts)), direct...)
}

func judgeSuite(spec suiteSpec, jobs []job, resultsDir, workspace string) (*suiteResult, []failure, []flake) {
	var j *job
	for i := range jobs {
		if jobs[i].Name == spec.name {
			j = &jobs[i]
		}
	}
	ran := j != nil && (j.Conclusion == "success" || j.Conclusion == "failure" || j.Conclusion == "timed_out")
	if !ran {
		conc := "missing"
		if j != nil {
			if conc = j.Conclusion; conc == "" {
				conc = "unfinished"
			}
		}
		return nil, []failure{{Suite: spec.key, Test: "suite did not run", File: ciFile, Message: "job " + spec.name + " " + conc}}, nil
	}

	var fails []failure
	var flakes []flake
	passed, testFails := 0, 0
	if spec.results {
		sf := readSuiteFiles(filepath.Join(resultsDir, "results-"+spec.key), spec.key, workspace)
		fails = append(fails, sf.problems...)
		for _, o := range sf.outcomes {
			switch o.status {
			case statusPass:
				passed++
			case statusFail:
				testFails++
				fails = append(fails, failure{spec.key, o.test, o.file, o.msg})
			case statusFlake:
				flakes = append(flakes, flake{spec.key, o.test, o.file})
			}
		}
		if sf.seen == 0 && !sf.unreadable {
			fails = append(fails, failure{spec.key, "no test results", ciFile, "job " + spec.name + " wrote no results"})
		}
	} else if j.Conclusion == "success" {
		passed = 1
	}
	if j.Conclusion != "success" {
		fails = append(fails, stepFailures(spec.key, j, testFails == 0)...)
	}
	return &suiteResult{Name: spec.key, Passed: passed, Failed: len(fails), Flaky: len(flakes)}, fails, flakes
}

// stepFailures lists failed steps (when no test failure explains the job) and the steps an earlier failure skipped.
func stepFailures(key string, j *job, listFailed bool) []failure {
	steps := append([]step(nil), j.Steps...)
	sort.SliceStable(steps, func(a, b int) bool { return steps[a].Number < steps[b].Number })
	var out []failure
	failedSteps, afterFailure := 0, false
	for _, s := range steps {
		switch {
		case s.Conclusion == "failure":
			failedSteps++
			afterFailure = true
			if listFailed {
				out = append(out, failure{key, s.Name, ciFile, "step failure"})
			}
		case afterFailure && s.Conclusion == "skipped" && !strings.HasPrefix(s.Name, "Post "):
			out = append(out, failure{key, s.Name, ciFile, "not run: an earlier step failed"})
		}
	}
	if listFailed && failedSteps == 0 {
		out = append(out, failure{key, "(job)", ciFile, "job " + j.Conclusion})
	}
	return out
}

// ---- whole run ----

func assemble(in input) *summary {
	s := &summary{
		runID: in.runID, sha: in.sha, started: in.started, finished: in.finished,
		suites: []suiteResult{}, failures: []failure{}, flakes: []flake{},
		sectionBodies: map[string]json.RawMessage{},
	}
	rank := map[string]int{}
	for i, spec := range in.suites {
		rank[spec.key] = i
		sr, fails, flakes := judgeSuite(spec, in.jobs, in.resultsDir, in.workspace)
		if sr != nil {
			s.suites = append(s.suites, *sr)
		}
		s.failures = append(s.failures, fails...)
		s.flakes = append(s.flakes, flakes...)
	}
	red := false
	for i, sec := range in.sections {
		rank[sec.name] = len(in.suites) + i
		body, msg := readSection(sec.path)
		if msg != "" {
			s.failures = append(s.failures, failure{sec.name, "section did not run", sec.path, msg})
			continue
		}
		s.sectionNames = append(s.sectionNames, sec.name)
		s.sectionBodies[sec.name] = body
		var m map[string]json.RawMessage
		_ = json.Unmarshal(body, &m)
		if string(bytes.TrimSpace(m["ok"])) != "true" {
			red = true
		}
	}
	sort.SliceStable(s.failures, func(a, b int) bool {
		x, y := s.failures[a], s.failures[b]
		if rank[x.Suite] != rank[y.Suite] {
			return rank[x.Suite] < rank[y.Suite]
		}
		if x.File != y.File {
			return x.File < y.File
		}
		return x.Test < y.Test
	})
	sort.SliceStable(s.flakes, func(a, b int) bool {
		x, y := s.flakes[a], s.flakes[b]
		if rank[x.Suite] != rank[y.Suite] {
			return rank[x.Suite] < rank[y.Suite]
		}
		if x.File != y.File {
			return x.File < y.File
		}
		return x.Test < y.Test
	})
	s.verdict = "green"
	if red || len(s.failures) > 0 {
		s.verdict = "red"
	}
	return s
}

// readSection returns the file's JSON object, or a reason it did not run.
func readSection(path string) (json.RawMessage, string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "section file not readable: " + firstLine(err.Error())
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, "section file is not a JSON object"
	}
	return json.RawMessage(bytes.TrimSpace(raw)), ""
}

func (s *summary) marshal() ([]byte, error) {
	type kv struct {
		k string
		v any
	}
	items := []kv{
		{"schema", 1}, {"run_id", s.runID}, {"sha", s.sha},
		{"started_at", s.started.Format(time.RFC3339)}, {"finished_at", s.finished.Format(time.RFC3339)},
		{"verdict", s.verdict}, {"suites", s.suites}, {"failures", s.failures}, {"flakes", s.flakes},
	}
	for _, n := range s.sectionNames {
		items = append(items, kv{n, s.sectionBodies[n]})
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, it := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(it.k)
		v, err := json.Marshal(it.v)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func (s *summary) report(w io.Writer) {
	fmt.Fprintf(w, "nightlysummary: %s, %d suites, %d failures, %d flakes\n", s.verdict, len(s.suites), len(s.failures), len(s.flakes))
	for _, f := range s.failures {
		fmt.Fprintf(w, "FAIL %s %s %s: %s\n", f.Suite, f.File, f.Test, f.Message)
	}
	for _, f := range s.flakes {
		fmt.Fprintf(w, "FLAKE %s %s %s\n", f.Suite, f.File, f.Test)
	}
}
