package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	sectionSuite = "hubspot"
	sectionFile  = "internal/tools/nightlycontacts"
	pollEvery    = 5 * time.Second
	checkLimit   = 3 * time.Minute
	leftLimit    = 2 * time.Minute
	demoSuffix   = " — demo request"
	signUpSuffix = " — sign-up"
)

const (
	stPortal     = "portal is the test account"
	stConfigured = "test portal is configured"
	stLeftovers  = "leftovers deleted"
	stRegistered = "registration accepted"
	stRegInHS    = "registrant in HubSpot"
	stRegInRS    = "registrant in Resend"
	stDemoSent   = "demo request accepted"
	stDemoDeal   = "demo deal in HubSpot"
	stDelHS      = "run contacts deleted from HubSpot"
	stDelRS      = "run contacts deleted from Resend"
	stNoneLeft   = "no run contact or deal left"
	stNoOther    = "no other new Resend contact"
	stDomain     = "receiving domain is valid"
)

// stepOrder is the section's step order.
var stepOrder = []string{stPortal, stConfigured, stLeftovers, stRegistered, stRegInHS, stRegInRS, stDemoSent, stDemoDeal, stDelHS, stDelRS, stNoneLeft, stNoOther}

var domainPattern = regexp.MustCompile(`^[a-z0-9-]+\.resend\.app$`)

// isRunContact is the one deletion rule, in both vendors: any @example.com address, and
// nightly-*@<receiving domain>. Any other address is a person.
func isRunContact(email, domain string) bool {
	e := strings.ToLower(email)
	return strings.HasSuffix(e, "@example.com") || (strings.HasPrefix(e, "nightly-") && strings.HasSuffix(e, "@"+domain))
}

// isRunDeal matches the names the deal worker writes, and the blanked name of an erase.
func isRunDeal(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, demoSuffix) || strings.HasSuffix(n, signUpSuffix) ||
		n == "demo request" || n == "sign-up" || n == "erased contact"
}

type stepResult struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Detail     string `json:"detail,omitempty"`
}

type failureEntry struct {
	Suite   string `json:"suite"`
	Test    string `json:"test"`
	File    string `json:"file"`
	Message string `json:"message"`
}

type sectionOut struct {
	OK       bool           `json:"ok"`
	Failures []failureEntry `json:"failures"`
	Steps    []stepResult   `json:"steps"`
}

type runner struct {
	v       *vendors
	domain  string
	stage   string
	gateway string
	runID   string
	steps   []stepResult
}

// stepFn returns "passed" or "skipped" with an optional note, or "failed" with the reason.
type stepFn func() (status, detail string)

func (r *runner) step(name string, fn stepFn) string {
	start := time.Now()
	status, detail := fn()
	r.steps = append(r.steps, stepResult{name, status, time.Since(start).Milliseconds(), detail})
	return status
}

func (r *runner) skip(detail string, names ...string) {
	for _, n := range names {
		r.steps = append(r.steps, stepResult{n, "skipped", 0, detail})
	}
}

// refuseDomain makes the phase's only step when RECEIVING_DOMAIN is outside A3's pattern. It runs before any vendor call.
func (r *runner) refuseDomain() bool {
	if domainPattern.MatchString(r.domain) {
		return false
	}
	r.step(stDomain, func() (string, string) { return "failed", "RECEIVING_DOMAIN is not <name>.resend.app" })
	return true
}

func (r *runner) guard() bool {
	ok, detail := r.v.portal()
	if !ok {
		r.step(stPortal, func() (string, string) { return "failed", detail })
	}
	return ok
}

func (r *runner) start() []stepResult {
	if r.refuseDomain() {
		return r.steps
	}
	if r.step(stPortal, func() (string, string) {
		if ok, d := r.v.portal(); !ok {
			return "failed", d
		}
		return "passed", ""
	}) != "passed" {
		r.skip("portal guard refused", stConfigured, stLeftovers)
		return r.steps
	}
	if r.step(stConfigured, func() (string, string) {
		if ok, d := r.v.configured(r.stage); !ok {
			return "failed", d
		}
		return "passed", ""
	}) != "passed" {
		r.skip("portal is not configured", stLeftovers)
		return r.steps
	}
	hs, hsErr := r.v.hsList("contacts", "email")
	rs, rsErr := r.v.rsList()
	r.step(stLeftovers, func() (string, string) {
		if hsErr != nil || rsErr != nil {
			return "failed", listDetail(hsErr, rsErr)
		}
		hd, hf := r.sweepHubSpot(hs)
		rd, rf := r.sweepResend(rs)
		d := fmt.Sprintf("HubSpot %d deleted, %d failed; Resend %d deleted, %d failed", hd, hf, rd, rf)
		if hf+rf > 0 {
			return "failed", d
		}
		return "passed", d
	})
	return r.steps
}

func listDetail(errs ...error) string {
	var parts []string
	for _, e := range errs {
		if e != nil {
			parts = append(parts, e.Error())
		}
	}
	return strings.Join(parts, "; ")
}

func (r *runner) sweepHubSpot(emails []string) (deleted, failed int) {
	for _, e := range emails {
		if !isRunContact(e, r.domain) {
			continue
		}
		if r.v.eraseHubSpot(e) != nil {
			failed++
		} else {
			deleted++
		}
	}
	return
}

func (r *runner) sweepResend(cs []resendContact) (deleted, failed int) {
	for _, c := range cs {
		if !isRunContact(c.Email, r.domain) {
			continue
		}
		if r.v.eraseResend(c.Email) != nil {
			failed++
		} else {
			deleted++
		}
	}
	return
}

// poll runs fn every pollEvery until it passes or limit of waiting is spent.
func (r *runner) poll(limit time.Duration, fn func() (bool, string)) (string, string) {
	for waited := time.Duration(0); ; waited += pollEvery {
		ok, detail := fn()
		if ok {
			return "passed", ""
		}
		if waited >= limit {
			return "failed", detail + " after " + limit.String()
		}
		r.v.sleep(pollEvery)
	}
}

func (r *runner) post(path string, body any) int {
	status, _ := r.v.do(http.MethodPost, strings.TrimRight(r.gateway, "/")+path, "", body)
	return status
}

func (r *runner) check() []stepResult {
	if r.refuseDomain() {
		return r.steps
	}
	if !r.guard() {
		r.skip("portal guard refused", stRegistered, stRegInHS, stRegInRS, stDemoSent, stDemoDeal)
		return r.steps
	}
	regEmail := fmt.Sprintf("nightly-%s-reg@%s", r.runID, r.domain)
	demoEmail := fmt.Sprintf("nightly-%s-demo@%s", r.runID, r.domain)

	registered := r.step(stRegistered, func() (string, string) {
		pw := randomHex(12)
		status := r.post("/auth/register", map[string]string{
			"email": regEmail, "password": pw, "workspace_name": "Nightly contacts", "display_name": "Nightly", "kind": "firm",
		})
		if status != http.StatusAccepted {
			return "failed", "register " + answered(status)
		}
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		state := base64.RawURLEncoding.EncodeToString(raw)
		if status = r.post("/auth/sign-in", map[string]string{"email": regEmail, "password": pw, "state": state}); status != http.StatusOK {
			return "failed", "sign-in " + answered(status)
		}
		return "passed", ""
	})
	if registered != "passed" {
		r.skip("registration failed", stRegInHS, stRegInRS)
	} else {
		r.step(stRegInHS, func() (string, string) {
			return r.poll(checkLimit, func() (bool, string) {
				_, tags, status := r.v.hsContact(regEmail)
				switch {
				case status != http.StatusOK:
					return false, "contact read " + answered(status)
				case !hasTag(tags, "registered"):
					return false, "contact lacks the registered tag"
				}
				return true, ""
			})
		})
		r.step(stRegInRS, func() (string, string) {
			return r.poll(checkLimit, func() (bool, string) {
				var c struct{}
				if status := r.v.rsGet("/contacts/"+url.PathEscape(regEmail), &c); status != http.StatusOK {
					return false, "contact read " + answered(status)
				}
				return true, ""
			})
		})
	}

	demo := r.step(stDemoSent, func() (string, string) {
		status := r.post("/contacts/demo-request", map[string]string{"email": demoEmail, "name": "Nightly Demo", "company": "Nightly"})
		if status != http.StatusAccepted {
			return "failed", "demo request " + answered(status)
		}
		return "passed", ""
	})
	if demo != "passed" {
		r.skip("demo request failed", stDemoDeal)
		return r.steps
	}
	r.step(stDemoDeal, func() (string, string) {
		return r.poll(checkLimit, func() (bool, string) {
			id, _, status := r.v.hsContact(demoEmail)
			if status != http.StatusOK {
				return false, "contact read " + answered(status)
			}
			return r.v.demoDeal(id, r.stage)
		})
	})
	return r.steps
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (r *runner) finish(in string, started time.Time) []stepResult {
	prior := loadPhases(in)
	if r.refuseDomain() {
		return mergeSteps(append(prior, r.steps...))
	}
	hsOK := r.guard()
	var hs []string
	var hsErr error
	if hsOK {
		hs, hsErr = r.v.hsList("contacts", "email")
	}
	rs, rsErr := r.v.rsList()

	if hsOK {
		r.step(stDelHS, func() (string, string) {
			if hsErr != nil {
				return "failed", hsErr.Error()
			}
			return deleteVerdict(r.sweepHubSpot(hs))
		})
	} else {
		r.skip("portal guard refused", stDelHS)
	}
	r.step(stDelRS, func() (string, string) {
		if rsErr != nil {
			return "failed", rsErr.Error()
		}
		return deleteVerdict(r.sweepResend(rs))
	})

	var last []resendContact
	var lastErr error
	r.step(stNoneLeft, func() (string, string) {
		return r.poll(leftLimit, func() (bool, string) {
			contacts, deals := 0, 0
			var errs []error
			if hsOK {
				emails, err := r.v.hsList("contacts", "email")
				errs = append(errs, err)
				for _, e := range emails {
					if isRunContact(e, r.domain) {
						contacts++
					}
				}
				names, err := r.v.hsList("deals", "dealname")
				errs = append(errs, err)
				for _, n := range names {
					if isRunDeal(n) {
						deals++
					}
				}
			}
			last, lastErr = r.v.rsList()
			errs = append(errs, lastErr)
			for _, c := range last {
				if isRunContact(c.Email, r.domain) {
					contacts++
				}
			}
			if d := listDetail(errs...); d != "" {
				return false, d
			}
			if contacts+deals > 0 {
				return false, fmt.Sprintf("%d contact(s) and %d deal(s) remain", contacts, deals)
			}
			return true, ""
		})
	})
	r.step(stNoOther, func() (string, string) {
		if lastErr != nil {
			return "failed", "Resend list unreadable"
		}
		n := 0
		for _, c := range last {
			if isRunContact(c.Email, r.domain) {
				continue
			}
			if t, ok := c.created(); !ok || !t.Before(started) {
				n++
			}
		}
		if n > 0 {
			return "failed", fmt.Sprintf("%d new contact(s) match no deletion rule; read the addresses in Resend", n)
		}
		return "passed", ""
	})
	return mergeSteps(append(prior, r.steps...))
}

func deleteVerdict(deleted, failed int) (string, string) {
	d := fmt.Sprintf("%d deleted, %d failed", deleted, failed)
	if failed > 0 {
		return "failed", d
	}
	return "passed", d
}

// loadPhases reads start.json and check.json from dir or one directory below it (an artifact download).
// A phase that is missing or unreadable gives a failed step.
func loadPhases(dir string) []stepResult {
	var out []stepResult
	for _, phase := range []string{"start", "check"} {
		var sec sectionOut
		found := false
		for _, pattern := range []string{phase + ".json", "*/" + phase + ".json"} {
			matches, _ := filepath.Glob(filepath.Join(dir, pattern))
			if len(matches) == 0 {
				continue
			}
			if raw, err := os.ReadFile(matches[0]); err == nil && json.Unmarshal(raw, &sec) == nil && len(sec.Steps) > 0 {
				found = true
				break
			}
		}
		if !found {
			out = append(out, stepResult{phase + " phase ran", "failed", 0, phase + ".json is missing or unreadable"})
			continue
		}
		out = append(out, sec.Steps...)
	}
	return out
}

// mergeSteps keeps the last step of each name, in stepOrder, then any other name in first-seen order.
func mergeSteps(all []stepResult) []stepResult {
	last := map[string]stepResult{}
	var extra []string
	known := map[string]bool{}
	for _, n := range stepOrder {
		known[n] = true
	}
	for _, s := range all {
		if _, seen := last[s.Name]; !seen && !known[s.Name] {
			extra = append(extra, s.Name)
		}
		last[s.Name] = s
	}
	var out []stepResult
	for _, n := range append(append([]string{}, stepOrder...), extra...) {
		if s, ok := last[n]; ok {
			out = append(out, s)
		}
	}
	return out
}

// writeSection prints each step, writes the section file and returns the exit code: 1 on any failed step.
func writeSection(path string, steps []stepResult, stdout, stderr io.Writer) int {
	sec := sectionOut{OK: true, Failures: []failureEntry{}, Steps: steps}
	for _, s := range steps {
		line := s.Status + " " + s.Name
		if s.Detail != "" {
			line += ": " + s.Detail
		}
		fmt.Fprintln(stdout, line)
		if s.Status == "failed" {
			sec.OK = false
			sec.Failures = append(sec.Failures, failureEntry{sectionSuite, s.Name, sectionFile, s.Detail})
		}
	}
	raw, err := json.MarshalIndent(sec, "", "  ")
	if err == nil {
		err = os.WriteFile(path, append(raw, '\n'), 0o644)
	}
	if err != nil {
		fmt.Fprintln(stderr, "nightlycontacts: cannot write the section file")
		return 1
	}
	if !sec.OK {
		return 1
	}
	return 0
}
