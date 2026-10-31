package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testToken  = "tok-secret-1"
	testKey    = "re_secretkey1"
	testDomain = "x.resend.app"
	testStage  = "appointmentscheduled"
	regAddr    = "nightly-77-2-reg@x.resend.app"
	demoAddr   = "nightly-77-2-demo@x.resend.app"
)

type call struct{ method, host, path, body string }

type hsContactRow struct{ id, email, tags string }
type hsDealRow struct{ id, name, stage, contactID string }
type rsContactRow struct{ id, email, created string }

// world is a fake HubSpot, Resend and gateway behind one RoundTripper.
type world struct {
	mu    sync.Mutex
	calls []call

	portalStatus int
	portalBody   string
	stages       []string
	propStatus   int
	pageSize     int

	hsContacts []hsContactRow
	hsDeals    []hsDealRow
	rsContacts []rsContactRow

	deleted        []string // "hs:<email>", "rs:<email>"
	noopDeletes    bool
	rsDeleteStatus int
	hsDeleteStatus int
	registerStatus int
	demoStatus     int
	hsListFail     bool // a list page after the first answers 500
	rsListFail     bool

	// check knobs
	noHS, noRS, noTag, noDeal bool
	dealStage, dealName       string
	hsMiss, rsMiss            int // reads of the registrant that answer 404 first

	// failure modes
	failAll  bool // every vendor call answers 500 with a body that echoes secrets
	netError bool // every call fails with an error that holds the URL

	firstDelete      int
	hsListEnd, rsEnd int
	nextID           int
}

func newWorld() *world {
	return &world{
		portalStatus: 200, portalBody: `{"portalId":999}`, stages: []string{testStage, "other"}, propStatus: 200,
		pageSize: 100, rsDeleteStatus: 200, hsDeleteStatus: 200, registerStatus: 202, demoStatus: 202, dealStage: testStage, dealName: "Nightly" + demoSuffix,
		firstDelete: -1, hsListEnd: -1, rsEnd: -1,
	}
}

func (w *world) id() string { w.nextID++; return strconv.Itoa(1000 + w.nextID) }

func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

func jsonReply(v any) *http.Response {
	b, _ := json.Marshal(v)
	return reply(200, string(b))
}

func (w *world) RoundTrip(req *http.Request) (*http.Response, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var body string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}
	path := req.URL.Path
	w.calls = append(w.calls, call{req.Method, req.URL.Host, path + "?" + req.URL.RawQuery, body})
	if w.netError {
		return nil, fmt.Errorf("dial %s with %s", req.URL, req.Header.Get("Authorization"))
	}
	if w.failAll {
		return reply(500, fmt.Sprintf(`{"message":"%s %s %s"}`, regAddr, testToken, testKey)), nil
	}
	switch req.URL.Host {
	case "api.hubapi.com":
		return w.hubspot(req, path, body), nil
	case "api.resend.com":
		return w.resend(req, path), nil
	}
	return w.gateway(req, path, body), nil
}

func (w *world) markDelete() {
	if w.firstDelete < 0 {
		w.firstDelete = len(w.calls)
	}
}

func (w *world) hubspot(req *http.Request, path, body string) *http.Response {
	q := req.URL.Query()
	const objs = "/crm/v3/objects/"
	switch {
	case req.Method == "GET" && path == "/account-info/v3/details":
		return reply(w.portalStatus, w.portalBody)
	case path == "/crm/v3/pipelines/deals/default/stages":
		var rs []map[string]string
		for _, s := range w.stages {
			rs = append(rs, map[string]string{"id": s})
		}
		return jsonReply(map[string]any{"results": rs})
	case strings.HasPrefix(path, "/crm/v3/properties/contacts/"):
		return reply(w.propStatus, `{}`)
	case req.Method == "POST" && path == objs+"contacts/gdpr-delete":
		w.markDelete()
		var b struct {
			ObjectID string `json:"objectId"`
		}
		_ = json.Unmarshal([]byte(body), &b)
		w.deleted = append(w.deleted, "hs:"+b.ObjectID)
		if w.hsDeleteStatus != 200 {
			return reply(w.hsDeleteStatus, `{}`)
		}
		if !w.noopDeletes {
			for i, c := range w.hsContacts {
				if c.email == b.ObjectID {
					w.hsContacts = append(w.hsContacts[:i], w.hsContacts[i+1:]...)
					break
				}
			}
		}
		return reply(200, `{}`)
	case req.Method == "GET" && path == objs+"contacts":
		var emails []string
		for _, c := range w.hsContacts {
			emails = append(emails, c.email)
		}
		if w.hsListFail && q.Get("after") != "" {
			return reply(500, `{}`)
		}
		return w.hsPage(q.Get("after"), emails, "email", &w.hsListEnd)
	case req.Method == "GET" && path == objs+"deals":
		var names []string
		for _, d := range w.hsDeals {
			names = append(names, d.name)
		}
		return w.hsPage(q.Get("after"), names, "dealname", new(int))
	case req.Method == "GET" && strings.HasPrefix(path, objs+"contacts/"):
		email := strings.TrimPrefix(path, objs+"contacts/")
		if email == regAddr && w.hsMiss > 0 {
			w.hsMiss--
			return reply(404, `{}`)
		}
		for _, c := range w.hsContacts {
			if c.email == email {
				return jsonReply(map[string]any{"id": c.id, "properties": map[string]string{"ascomply_contact_tags": c.tags}})
			}
		}
		return reply(404, `{}`)
	case req.Method == "GET" && strings.Contains(path, "/crm/v4/objects/contacts/"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/crm/v4/objects/contacts/"), "/associations/deals")
		var rs []map[string]string
		for _, d := range w.hsDeals {
			if d.contactID == id {
				rs = append(rs, map[string]string{"toObjectId": d.id})
			}
		}
		return jsonReply(map[string]any{"results": rs})
	case strings.HasPrefix(path, objs+"deals/"):
		id := strings.TrimPrefix(path, objs+"deals/")
		for i, d := range w.hsDeals {
			if d.id != id {
				continue
			}
			switch req.Method {
			case "GET":
				return jsonReply(map[string]any{"properties": map[string]string{"dealname": d.name, "dealstage": d.stage}})
			case "DELETE":
				w.hsDeals = append(w.hsDeals[:i], w.hsDeals[i+1:]...)
				return reply(204, ``)
			}
			return reply(200, `{}`)
		}
		return reply(404, `{}`)
	}
	return reply(404, `{}`)
}

func (w *world) hsPage(after string, vals []string, prop string, end *int) *http.Response {
	off, _ := strconv.Atoi(after)
	hi := min(off+w.pageSize, len(vals))
	var rs []map[string]any
	for _, v := range vals[off:hi] {
		rs = append(rs, map[string]any{"properties": map[string]string{prop: v}})
	}
	out := map[string]any{"results": rs}
	if hi < len(vals) {
		out["paging"] = map[string]any{"next": map[string]string{"after": strconv.Itoa(hi)}}
	} else if *end < 0 {
		*end = len(w.calls)
	}
	return jsonReply(out)
}

func (w *world) resend(req *http.Request, path string) *http.Response {
	switch {
	case req.Method == "GET" && path == "/contacts":
		after := req.URL.Query().Get("after")
		if w.rsListFail && after != "" {
			return reply(500, `{}`)
		}
		start := 0
		for i, c := range w.rsContacts {
			if c.id == after {
				start = i + 1
			}
		}
		hi := min(start+w.pageSize, len(w.rsContacts))
		var data []map[string]string
		for _, c := range w.rsContacts[start:hi] {
			data = append(data, map[string]string{"id": c.id, "email": c.email, "created_at": c.created})
		}
		if hi >= len(w.rsContacts) && w.rsEnd < 0 {
			w.rsEnd = len(w.calls)
		}
		return jsonReply(map[string]any{"has_more": hi < len(w.rsContacts), "data": data})
	case req.Method == "GET":
		email := strings.TrimPrefix(path, "/contacts/")
		if email == regAddr && w.rsMiss > 0 {
			w.rsMiss--
			return reply(404, `{}`)
		}
		for _, c := range w.rsContacts {
			if c.email == email {
				return reply(200, `{}`)
			}
		}
		return reply(404, `{}`)
	case req.Method == "DELETE":
		w.markDelete()
		email := strings.TrimPrefix(path, "/contacts/")
		w.deleted = append(w.deleted, "rs:"+email)
		if w.rsDeleteStatus != 200 {
			return reply(w.rsDeleteStatus, `{}`)
		}
		if !w.noopDeletes {
			for i, c := range w.rsContacts {
				if c.email == email {
					w.rsContacts = append(w.rsContacts[:i], w.rsContacts[i+1:]...)
					break
				}
			}
		}
		return reply(200, `{}`)
	}
	return reply(404, `{}`)
}

func (w *world) gateway(req *http.Request, path, body string) *http.Response {
	var b map[string]string
	_ = json.Unmarshal([]byte(body), &b)
	switch path {
	case "/auth/register":
		return reply(w.registerStatus, `{}`)
	case "/auth/sign-in":
		if !w.noHS {
			tags := ";registered"
			if w.noTag {
				tags = ""
			}
			w.hsContacts = append(w.hsContacts, hsContactRow{w.id(), b["email"], tags})
		}
		if !w.noRS {
			w.rsContacts = append(w.rsContacts, rsContactRow{w.id(), b["email"], "2026-10-31T02:00:00Z"})
		}
		return reply(200, `{"code":"c"}`)
	case "/contacts/demo-request":
		if w.demoStatus != 202 {
			return reply(w.demoStatus, fmt.Sprintf(`{"message":"%s"}`, b["email"]))
		}
		c := hsContactRow{w.id(), b["email"], ";demo_request"}
		w.hsContacts = append(w.hsContacts, c)
		if !w.noDeal {
			w.hsDeals = append(w.hsDeals, hsDealRow{w.id(), w.dealName, w.dealStage, c.id})
		}
		return reply(w.demoStatus, `{}`)
	}
	return reply(404, `{}`)
}

func (w *world) count(host string) (n int) {
	for _, c := range w.calls {
		if c.host == host {
			n++
		}
	}
	return
}

func (w *world) bodyOf(path string) string {
	for _, c := range w.calls {
		if strings.HasPrefix(c.path, path) {
			return c.body
		}
	}
	return ""
}

func baseEnv() map[string]string {
	return map[string]string{
		"HUBSPOT_TOKEN": testToken, "RESEND_API_KEY": testKey, "RECEIVING_DOMAIN": testDomain,
		"HUBSPOT_DEAL_STAGE": testStage, "GATEWAY_URL": "https://gw.test", "NIGHTLY_RUN_ID": "77-2",
	}
}

type result struct {
	code   int
	stdout string
	stderr string
	sec    sectionOut
	raw    string
	sleeps []time.Duration
}

func (r result) step(name string) stepResult {
	for _, s := range r.sec.Steps {
		if s.Name == name {
			return s
		}
	}
	return stepResult{Name: name, Status: "absent"}
}

func (r result) names() (out []string) {
	for _, s := range r.sec.Steps {
		out = append(out, s.Name)
	}
	return
}

func runPhase(t *testing.T, w *world, env map[string]string, args ...string) result {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.json")
	var stdout, stderr bytes.Buffer
	var sleeps []time.Duration
	getenv := func(k string) string { return env[k] }
	if !slices.Contains(args, "-out") {
		args = append(args, "-out", out)
	} else {
		out = args[slices.Index(args, "-out")+1]
	}
	code := runWith(args, getenv, w, func(d time.Duration) { sleeps = append(sleeps, d) }, &stdout, &stderr)
	res := result{code: code, stdout: stdout.String(), stderr: stderr.String(), sleeps: sleeps}
	if raw, err := os.ReadFile(out); err == nil {
		res.raw = string(raw)
		if err := json.Unmarshal(raw, &res.sec); err != nil {
			t.Fatalf("section is not JSON: %v", err)
		}
	}
	return res
}

const startedAt = "2026-10-31T01:00:00Z"

// writePhases writes a green start.json and check.json.
func writePhases(t *testing.T, dir string) {
	t.Helper()
	for phase, names := range map[string][]string{
		"start": {stPortal, stConfigured, stLeftovers},
		"check": {stRegistered, stRegInHS, stRegInRS, stDemoSent, stDemoDeal},
	} {
		sec := sectionOut{OK: true, Failures: []failureEntry{}}
		for _, n := range names {
			sec.Steps = append(sec.Steps, stepResult{Name: n, Status: "passed"})
		}
		raw, _ := json.Marshal(sec)
		if err := os.WriteFile(filepath.Join(dir, phase+".json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func finish(t *testing.T, w *world, env map[string]string) result {
	t.Helper()
	dir := t.TempDir()
	writePhases(t, dir)
	return runPhase(t, w, env, "finish", "-in", dir, "-started", startedAt)
}
