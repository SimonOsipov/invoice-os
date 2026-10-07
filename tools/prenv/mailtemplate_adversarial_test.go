// mailtemplate_adversarial_test.go pins what GoTrue does that the AC tables do not: redirects, bodies
// of every shape, both execution branches, and the logo check's default URL.
package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

func redirectServer(t *testing.T, to string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, to, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// GoTrue's http.DefaultClient follows redirects, so the check's verdict is the final answer's.
func TestMailTemplateCheck_FollowsRedirectsLikeGoTrue(t *testing.T) {
	good := htmlServer(t, mailMinimalTemplate)
	spa := htmlServer(t, mailSPA)
	missing := mailServer(t, http.StatusNotFound, "text/html", mailMinimalTemplate)
	loop := redirectServer(t, "/again")

	t.Run("a redirect to a serving template passes", func(t *testing.T) {
		from := redirectServer(t, good.URL)
		code, out := runMailCheck(t, &http.Client{}, from.URL)
		wantOK(t, code, out, from.URL)
	})
	t.Run("a redirect to the SPA fails", func(t *testing.T) {
		from := redirectServer(t, spa.URL)
		code, out := runMailCheck(t, &http.Client{}, from.URL)
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		wantOneError(t, out, from.URL, confURLRE)
	})
	t.Run("a redirect to a 404 fails", func(t *testing.T) {
		from := redirectServer(t, missing.URL)
		code, out := runMailCheck(t, &http.Client{}, from.URL)
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		wantOneError(t, out, from.URL, regexp.MustCompile(`404`))
	})
	t.Run("a redirect loop fails", func(t *testing.T) {
		code, out := runMailCheck(t, &http.Client{}, loop.URL)
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		wantOneError(t, out, loop.URL, regexp.MustCompile(`.`))
	})
}

// The built binary's own client must follow redirects as GoTrue's does.
func TestMailTemplateCheck_CLIFollowsRedirects(t *testing.T) {
	good := htmlServer(t, mailMinimalTemplate)
	from := redirectServer(t, good.URL)
	stdout, stderr, code := runCLI(t, "mail-template-check", from.URL)
	if code != 0 {
		t.Errorf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "ok "+from.URL) {
		t.Errorf("stdout lacks `ok %s`: %q", from.URL, stdout)
	}
}

// GoTrue reads the body and never looks at the content type.
func TestMailTemplateCheck_ContentTypeIsNotChecked(t *testing.T) {
	for _, ct := range []string{"application/json", "text/plain", "application/octet-stream", "image/png"} {
		t.Run(ct, func(t *testing.T) {
			srv := mailServer(t, http.StatusOK, ct, mailMinimalTemplate)
			code, out := runMailCheck(t, srv.Client(), srv.URL)
			wantOK(t, code, out, srv.URL)
		})
	}
}

func TestMailTemplateCheck_EmptyBodyFails(t *testing.T) {
	srv := htmlServer(t, "")
	code, out := runMailCheck(t, srv.Client(), srv.URL)
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	wantOneError(t, out, srv.URL, confURLRE)
}

// chunkedServer streams body with no Content-Length, so the size is known only by reading.
func chunkedServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		f := w.(http.Flusher)
		for len(body) > 0 {
			n := min(len(body), 64*1024)
			_, _ = io.WriteString(w, body[:n])
			f.Flush()
			body = body[n:]
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMailTemplateCheck_ChunkedBodyAtTheLimit(t *testing.T) {
	t.Run("exactly the limit passes", func(t *testing.T) {
		srv := chunkedServer(t, padded(t, goTrueTemplateMaxSize))
		code, out := runMailCheck(t, srv.Client(), srv.URL)
		wantOK(t, code, out, srv.URL)
	})
	t.Run("one byte over fails", func(t *testing.T) {
		srv := chunkedServer(t, padded(t, goTrueTemplateMaxSize+1))
		code, out := runMailCheck(t, srv.Client(), srv.URL)
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		wantOneError(t, out, srv.URL, sizeLimRE)
	})
}

// A body that never ends is cut at the limit, not read to the timeout.
func TestMailTemplateCheck_EndlessBodyFailsAtTheLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, mailMinimalTemplate+"<!--")
		chunk := []byte(strings.Repeat("a", 64*1024))
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	start := time.Now()
	code, out := runMailCheck(t, srv.Client(), srv.URL)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	wantOneError(t, out, srv.URL, sizeLimRE)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the check took %s, want it cut at the size limit well before the 10 s timeout", d)
	}
}

// GoTrue times a fetch out at 10 s; the check does too, body included.
func TestMailTemplateCheck_HungServerTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 10 s request timeout")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		case <-time.After(20 * time.Second):
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	start := time.Now()
	code, out := runMailCheck(t, &http.Client{}, srv.URL)
	took := time.Since(start)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	wantOneError(t, out, srv.URL, regexp.MustCompile(`(?i)deadline|timeout|timed out`))
	if took < 9*time.Second || took > 13*time.Second {
		t.Errorf("the request gave up after %s, want about 10 s as GoTrue does", took)
	}
}

// An <img> inside a template branch is still the image rule's subject.
func TestMailTemplateCheck_ImageInsideABranchIsChecked(t *testing.T) {
	imgs := newImageServer(t)
	tpl := htmlServer(t, mailMinimalTemplate+`{{ if .Data.registration }}<img src="assets/mark.png">{{ end }}`)
	code, out := runMailCheck(t, imgs.Client(), tpl.URL)
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	wantOneError(t, out, "assets/mark.png", regexp.MustCompile(`.`))
}

// The confirmation URL must be in every execution, not only the answered one.
func TestMailTemplateCheck_ConfirmationURLInEveryBranch(t *testing.T) {
	cases := map[string]string{
		"only with the answers":    `{{ if .Data.registration }}{{ .ConfirmationURL }}{{ end }}`,
		"only without the answers": `{{ if not .Data.registration }}{{ .ConfirmationURL }}{{ end }}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := htmlServer(t, body)
			code, out := runMailCheck(t, srv.Client(), srv.URL)
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			wantOneError(t, out, srv.URL, confURLRE)
		})
	}
}

// An <img> whose src the scan cannot read is an image it did not check.
func TestMailTemplateCheck_ImageSrcSpellings(t *testing.T) {
	imgs := newImageServer(t)
	cases := map[string]string{
		"single-quoted":                `<img src='assets/mark.png'>`,
		"upper-case tag and attribute": `<IMG SRC="assets/mark.png">`,
		"spaces around the equals":     `<img src = "assets/mark.png">`,
		"attributes before src":        `<img alt="x" width="36" src="assets/mark.png">`,
		"a newline before src":         "<img\n  alt=\"x\"\n  src=\"assets/mark.png\">",
		"a src= inside an alt value":   `<img alt="a src=https://x.test/z.png" src="assets/mark.png">`,
	}
	for name, img := range cases {
		t.Run(name, func(t *testing.T) {
			tpl := htmlServer(t, mailMinimalTemplate+img)
			code, out := runMailCheck(t, imgs.Client(), tpl.URL)
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			wantOneError(t, out, "assets/mark.png", regexp.MustCompile(`.`))
		})
	}
}

// html/template writes the & of an image URL as &amp;; the check asks for the unescaped URL.
func TestMailTemplateCheck_ImageURLIsUnescapedBeforeItIsFetched(t *testing.T) {
	var query atomic.Value
	imgs := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.RawQuery)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(imgs.Close)
	tpl := htmlServer(t, mailMinimalTemplate+`<img src="`+imgs.URL+`/m.png?a=1&amp;b=2">`)

	code, out := runMailCheck(t, imgs.Client(), tpl.URL)

	wantOK(t, code, out, tpl.URL)
	if got, _ := query.Load().(string); got != "a=1&b=2" {
		t.Errorf("the image was fetched with query %q, want %q", got, "a=1&b=2")
	}
}

func TestMailTemplateCheck_EachImageIsFetchedOnce(t *testing.T) {
	imgs := newImageServer(t)
	img := `<img src="` + imgs.logoURL() + `">`
	tpl := htmlServer(t, mailMinimalTemplate+img+img+img)

	code, out := runMailCheck(t, imgs.Client(), tpl.URL)

	wantOK(t, code, out, tpl.URL)
	if n := imgs.hitsOn("/emails/mark.png"); n != 1 {
		t.Errorf("the logo was fetched %d times, want 1", n)
	}
}

func TestMailTemplateCheck_MalformedArgumentSpellings(t *testing.T) {
	for name, urls := range map[string][]string{
		"http prefix without the scheme separator": {"httpfoo/x"},
		"a good URL then a bad one":                {"http://127.0.0.1:1/", "ftp://x"},
		"an empty argument":                        {""},
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

type recordTransport struct {
	urls []string
	resp func(*http.Request) *http.Response
}

func (r *recordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.urls = append(r.urls, req.URL.String())
	return r.resp(req), nil
}

func imageResponse(status int, ct string) func(*http.Request) *http.Response {
	return func(req *http.Request) *http.Response {
		h := http.Header{}
		h.Set("Content-Type", ct)
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader("x")), Request: req}
	}
}

// AC 9: the no-argument command checks accountmail.LogoURL, nothing else.
func TestMailLogoCheck_DefaultChecksAccountMailLogoURL(t *testing.T) {
	t.Run("a serving logo passes", func(t *testing.T) {
		rt := &recordTransport{resp: imageResponse(200, "image/png")}
		var out bytes.Buffer
		code := runMailLogoCheckDefault(&http.Client{Transport: rt}, &out)

		if code != 0 {
			t.Errorf("exit %d, want 0; output = %q", code, out.String())
		}
		if len(rt.urls) != 1 || rt.urls[0] != accountmail.LogoURL {
			t.Errorf("requested %v, want exactly [%s]", rt.urls, accountmail.LogoURL)
		}
		if !strings.Contains(out.String(), "ok "+accountmail.LogoURL) {
			t.Errorf("output lacks `ok %s`: %q", accountmail.LogoURL, out.String())
		}
	})
	t.Run("a 404 fails naming the logo", func(t *testing.T) {
		rt := &recordTransport{resp: imageResponse(404, "text/plain")}
		var out bytes.Buffer
		code := runMailLogoCheckDefault(&http.Client{Transport: rt}, &out)

		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out.String())
		}
		wantOneError(t, out.String(), accountmail.LogoURL, regexp.MustCompile(`404`))
	})
}

// AC 9: the command takes no argument; a stray one is a malformed call, made before any request.
func TestMailLogoCheck_CLIRejectsAnArgument(t *testing.T) {
	_, stderr, code := runCLI(t, "mail-logo-check", "https://example.com/mark.png")
	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if strings.Contains(stderr, "unknown subcommand") {
		t.Errorf("subcommand is not wired into main: %q", stderr)
	}
}

func TestMailTemplateCheck_ImageInsideABranchIsChecked_NoAnswers(t *testing.T) {
	imgs := newImageServer(t)
	bad := `<img src="assets/mark.png">`
	good := `<img src="` + imgs.logoURL() + `">`
	cases := map[string]string{
		"a bad image only when the answers are absent": `{{ if not .Data.registration }}` + bad + `{{ end }}`,
		"a bad image in the else branch":               `{{ if .Data.registration }}` + good + `{{ else }}` + bad + `{{ end }}`,
	}
	for name, branch := range cases {
		t.Run(name, func(t *testing.T) {
			tpl := htmlServer(t, mailMinimalTemplate+branch)
			code, out := runMailCheck(t, imgs.Client(), tpl.URL)
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			wantOneError(t, out, "assets/mark.png", regexp.MustCompile(`.`))
		})
	}
}

func TestMailTemplateCheck_ImageSrcSpellings_Unread(t *testing.T) {
	imgs := newImageServer(t)
	cases := map[string]string{
		"a data-src before the real src":        `<img data-src="` + imgs.logoURL() + `" src="assets/mark.png">`,
		"an unquoted src":                       `<img src=assets/mark.png>`,
		"src right after a quote":               `<img alt="x"src="assets/mark.png">`,
		"src after a slash":                     `<img/src=assets/mark.png>`,
		"src right after a single-quoted value": `<img alt='x'src="assets/mark.png">`,
	}
	for name, img := range cases {
		t.Run(name, func(t *testing.T) {
			tpl := htmlServer(t, mailMinimalTemplate+img)
			code, out := runMailCheck(t, imgs.Client(), tpl.URL)
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			wantOneError(t, out, "assets/mark.png", regexp.MustCompile(`.`))
		})
	}
}
