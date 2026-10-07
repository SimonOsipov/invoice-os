// mailtemplate_test.go pins prenv mail-template-check and mail-logo-check against httptest servers.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

const (
	// GoTrue v2.197.0 conf TemplateMaxSize; its io.LimitReader reads this many bytes.
	goTrueTemplateMaxSize = 1_000_000

	// The sample ConfirmationURL; html/template writes its & as &amp;.
	mailSampleURL = "https://x.test/verify?token=t&type=signup"

	mailMinimalTemplate = `<p>{{ .ConfirmationURL }}</p>`
	mailSPA             = `<!doctype html><div id="root"></div>`
)

var (
	pngBytes   = []byte("\x89PNG\r\n\x1a\n")
	mailErrRE  = regexp.MustCompile(`::error::`)
	confURLRE  = regexp.MustCompile(`(?i)confirmation ?url`)
	sizeLimRE  = regexp.MustCompile(`(?i)size|limit|too (large|big)|1,?000,?000`)
	parseErrRE = regexp.MustCompile(`(?i)pars`)
	execErrRE  = regexp.MustCompile(`(?i)execut`)
)

// mailServer serves one fixed response on every path.
func mailServer(t *testing.T, status int, contentType, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func htmlServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return mailServer(t, http.StatusOK, "text/html; charset=utf-8", body)
}

// imageServer is a TLS server: /emails/mark.png is a 200 image/png, /missing a 404, /page a 200 text/html.
// Its Client trusts its certificate and also dials plain HTTP.
type imageServer struct {
	*httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func newImageServer(t *testing.T) *imageServer {
	t.Helper()
	s := &imageServer{hits: map[string]int{}}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		switch r.URL.Path {
		case "/emails/mark.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *imageServer) hitsOn(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func (s *imageServer) logoURL() string { return s.URL + "/emails/mark.png" }

func runMailCheck(t *testing.T, client *http.Client, urls ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := RunMailTemplateCheck(client, urls, &out)
	return code, out.String()
}

func linesWith(out, needle string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) {
			got = append(got, l)
		}
	}
	return got
}

// wantOneError fails unless out has exactly one ::error:: line, naming url and matching reason.
func wantOneError(t *testing.T, out, url string, reason *regexp.Regexp) {
	t.Helper()
	errs := linesWith(out, "::error::")
	if len(errs) != 1 {
		t.Fatalf("want exactly one ::error:: line, got %d; output = %q", len(errs), out)
	}
	if !strings.Contains(errs[0], url) {
		t.Errorf("error line %q does not name %s", errs[0], url)
	}
	if !reason.MatchString(errs[0]) {
		t.Errorf("error line %q does not match %v", errs[0], reason)
	}
}

func wantOK(t *testing.T, code int, out, url string) {
	t.Helper()
	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, out)
	}
	if got := linesWith(out, "ok "+url); len(got) != 1 {
		t.Errorf("want exactly one `ok %s` line, got %d; output = %q", url, len(got), out)
	}
	if mailErrRE.MatchString(out) {
		t.Errorf("passing run printed an ::error:: line: %q", out)
	}
}

// AC 1.
func TestMailTemplateCheck_RealTemplatePasses(t *testing.T) {
	src, err := accountmail.Template("confirmation")
	if err != nil {
		t.Fatal(err)
	}
	imgs := newImageServer(t)
	body := strings.ReplaceAll(string(src), accountmail.LogoURL, imgs.logoURL())
	if body == string(src) {
		t.Fatalf("the real template carries no %s to rewrite onto the TLS server", accountmail.LogoURL)
	}
	tpl := htmlServer(t, body)
	url := tpl.URL + "/emails/confirmation.html"

	code, out := runMailCheck(t, imgs.Client(), url)

	wantOK(t, code, out, url)
	if imgs.hitsOn("/emails/mark.png") == 0 {
		t.Error("the check never fetched the template's logo; the image rule is unenforced")
	}
}

// AC 2.
func TestMailTemplateCheck_SPAFails(t *testing.T) {
	srv := htmlServer(t, mailSPA)
	code, out := runMailCheck(t, srv.Client(), srv.URL)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	wantOneError(t, out, srv.URL, confURLRE)
}

// AC 3.
func TestMailTemplateCheck_Non200Fails(t *testing.T) {
	srv := mailServer(t, http.StatusNotFound, "text/html", mailMinimalTemplate)
	code, out := runMailCheck(t, srv.Client(), srv.URL)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	wantOneError(t, out, srv.URL, regexp.MustCompile(`404`))
}

// AC 3.
func TestMailTemplateCheck_ParseErrorFails(t *testing.T) {
	srv := htmlServer(t, `{{ .ConfirmationURL`)
	code, out := runMailCheck(t, srv.Client(), srv.URL)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	wantOneError(t, out, srv.URL, parseErrRE)
}

// AC 3. A `{{ .Nope.deeper }}` body does not error under Go 1.26 (a missing map key
// renders empty), so the rows use bodies that do: `len 3` always, `len .Data.registration`
// only when the answers are absent.
func TestMailTemplateCheck_ExecuteErrorFails(t *testing.T) {
	cases := map[string]string{
		"always":                   `{{ .ConfirmationURL }}{{ len 3 }}`,
		"only without the answers": `{{ .ConfirmationURL }}{{ len .Data.registration }}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := htmlServer(t, body)
			code, out := runMailCheck(t, srv.Client(), srv.URL)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			wantOneError(t, out, srv.URL, execErrRE)
		})
	}
}

// padded returns a template that is exactly n bytes and renders the ConfirmationURL.
func padded(t *testing.T, n int) string {
	t.Helper()
	head, tail := mailMinimalTemplate+"<!--", "-->"
	fill := n - len(head) - len(tail)
	if fill < 0 {
		t.Fatalf("cannot pad to %d bytes", n)
	}
	body := head + strings.Repeat("a", fill) + tail
	if len(body) != n {
		t.Fatalf("padded body is %d bytes, want %d", len(body), n)
	}
	return body
}

// AC 3, boundary: GoTrue's LimitReader reads exactly TemplateMaxSize bytes.
func TestMailTemplateCheck_OversizeFails(t *testing.T) {
	t.Run("exactly the limit passes", func(t *testing.T) {
		srv := htmlServer(t, padded(t, goTrueTemplateMaxSize))
		code, out := runMailCheck(t, srv.Client(), srv.URL)
		wantOK(t, code, out, srv.URL)
	})
	t.Run("one byte over fails", func(t *testing.T) {
		srv := htmlServer(t, padded(t, goTrueTemplateMaxSize+1))
		code, out := runMailCheck(t, srv.Client(), srv.URL)

		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		wantOneError(t, out, srv.URL, sizeLimRE)
	})
}

// AC 4.
func TestMailTemplateCheck_ImageRules(t *testing.T) {
	imgs := newImageServer(t)
	plain := mailServer(t, http.StatusOK, "image/png", string(pngBytes))
	cases := []struct {
		name, img string
		wantCode  int
	}{
		{"https image answering 200 image/png passes", imgs.logoURL(), 0},
		{"no image at all passes", "", 0},
		{"relative src", "assets/mark.png", 1},
		{"http src that answers 200 image/png", plain.URL + "/mark.png", 1},
		{"https src answering 404", imgs.URL + "/missing", 1},
		{"https src answering text/html", imgs.URL + "/page", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := mailMinimalTemplate
			if c.img != "" {
				body += `<img src="` + c.img + `">`
			}
			tpl := htmlServer(t, body)
			code, out := runMailCheck(t, imgs.Client(), tpl.URL)

			if c.wantCode == 0 {
				wantOK(t, code, out, tpl.URL)
				return
			}
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			wantOneError(t, out, c.img, regexp.MustCompile(`.`))
		})
	}
}

// AC 5.
func TestMailTemplateCheck_MalformedCallExits2(t *testing.T) {
	for name, urls := range map[string][]string{
		"no argument":      nil,
		"ftp scheme":       {"ftp://x"},
		"no scheme at all": {"example.com/confirmation.html"},
	} {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: failTransport{t}}
			var out bytes.Buffer
			if code := RunMailTemplateCheck(client, urls, &out); code != 2 {
				t.Errorf("exit %d, want 2; output = %q", code, out.String())
			}
		})
	}
}

type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("a malformed call made a request to %s", r.URL)
	return nil, errors.New("no network")
}

// AC 5, through the built binary: the call reaches RunMailTemplateCheck, not the unknown-subcommand path.
func TestMailTemplateCheck_CLI(t *testing.T) {
	t.Run("usage names both subcommands", func(t *testing.T) {
		_, stderr, _ := runCLI(t)
		for _, sub := range []string{"mail-template-check", "mail-logo-check"} {
			if !strings.Contains(stderr, sub) {
				t.Errorf("usage lacks %q; stderr = %q", sub, stderr)
			}
		}
	})
	for name, args := range map[string][]string{
		"no argument": {"mail-template-check"},
		"ftp scheme":  {"mail-template-check", "ftp://x"},
	} {
		t.Run(name+" exits 2", func(t *testing.T) {
			_, stderr, code := runCLI(t, args...)
			if code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if strings.Contains(stderr, "unknown subcommand") {
				t.Errorf("subcommand is not wired into main: %q", stderr)
			}
		})
	}
	t.Run("a serving URL exits 0", func(t *testing.T) {
		srv := htmlServer(t, mailMinimalTemplate)
		stdout, stderr, code := runCLI(t, "mail-template-check", srv.URL)
		if code != 0 {
			t.Errorf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "ok "+srv.URL) {
			t.Errorf("stdout lacks `ok %s`: %q", srv.URL, stdout)
		}
	})
}

// AC 1 and 3 (mixed): one failure fails the run, and every URL still gets its line.
func TestMailTemplateCheck_OneFailureFailsTheRun(t *testing.T) {
	good := htmlServer(t, mailMinimalTemplate)
	spa := htmlServer(t, mailSPA)
	code, out := runMailCheck(t, good.Client(), good.URL, spa.URL)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	if got := linesWith(out, "ok "+good.URL); len(got) != 1 {
		t.Errorf("want one `ok %s` line, got %d; output = %q", good.URL, len(got), out)
	}
	wantOneError(t, out, spa.URL, confURLRE)
	if got := linesWith(out, "ok "+spa.URL); len(got) != 0 {
		t.Errorf("the failing URL also printed an ok line: %q", out)
	}
	if lines := nonBlankLines(out); len(lines) != 2 {
		t.Errorf("want one line per URL, got %d: %q", len(lines), out)
	}
}

// AC 11: html/template writes the & of the sample URL as &amp;; the check compares the unescaped output.
func TestMailTemplateCheck_ConfirmationURLWithAmpersand(t *testing.T) {
	escaped := strings.ReplaceAll(mailSampleURL, "&", "&amp;")
	i := strings.Index(mailSampleURL, "&")
	if escaped == mailSampleURL {
		t.Fatal("the sample URL carries no &; the case proves nothing")
	}
	cases := map[string]string{
		"rendered in text":        mailMinimalTemplate,
		"rendered in an href":     `<a href="{{ .ConfirmationURL }}">Confirm</a>`,
		"the sample, pre-escaped": `<a href="` + escaped + `">Confirm</a>`,
		"the check's sample has the & where the plan puts it": fmt.Sprintf(
			`{{ if eq (slice .ConfirmationURL %d %d) "&" }}{{ .ConfirmationURL }}{{ end }}`, i, i+1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := htmlServer(t, body)
			code, out := runMailCheck(t, srv.Client(), srv.URL)
			wantOK(t, code, out, srv.URL)
		})
	}
}

func runLogoCheck(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := RunMailLogoCheck(client, url, &out)
	return code, out.String()
}

// AC 9.
func TestMailLogoCheck_ImageRules(t *testing.T) {
	imgs := newImageServer(t)
	plain := mailServer(t, http.StatusOK, "image/png", string(pngBytes))
	cases := []struct {
		name, url string
		wantCode  int
	}{
		{"200 image/png", imgs.logoURL(), 0},
		{"404", imgs.URL + "/missing", 1},
		{"200 text/html", imgs.URL + "/page", 1},
		{"http scheme", plain.URL + "/mark.png", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out := runLogoCheck(t, imgs.Client(), c.url)

			if c.wantCode == 0 {
				wantOK(t, code, out, c.url)
				return
			}
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			wantOneError(t, out, c.url, regexp.MustCompile(`.`))
		})
	}
}
