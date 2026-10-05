package main

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

const (
	// GoTrue v2.197.0 conf/configuration.go TemplateMaxSize: the bytes its io.LimitReader reads.
	templateMaxSize = 1_000_000
	// GoTrue fetches a template with a 10 s timeout.
	mailRequestTimeout = 10 * time.Second
	// html/template writes the & of this URL as &amp;, so the check compares unescaped output.
	mailSampleConfirmationURL = "https://x.test/verify?token=t&type=signup"
)

var imgSrcRE = regexp.MustCompile(`(?is)<img\b[^>]*?\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)')`)

func isHTTPURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// RunMailTemplateCheck applies D12 to each URL: exit 0 all pass, 1 any fails, 2 malformed call.
func RunMailTemplateCheck(client *http.Client, urls []string, out io.Writer) int {
	if len(urls) == 0 {
		fmt.Fprintln(out, "::error::usage: prenv mail-template-check <http(s)-url>...")
		return 2
	}
	for _, u := range urls {
		if !isHTTPURL(u) {
			fmt.Fprintf(out, "::error::%s: not an http:// or https:// URL\n", u)
			return 2
		}
	}
	code := 0
	for _, u := range urls {
		if err := checkMailTemplate(client, u); err != nil {
			fmt.Fprintf(out, "::error::%s: %v\n", u, err)
			code = 1
			continue
		}
		fmt.Fprintf(out, "ok %s\n", u)
	}
	return code
}

// RunMailLogoCheck applies D12's image rule to one URL: exit 0 pass, 1 fail.
func RunMailLogoCheck(client *http.Client, url string, out io.Writer) int {
	if err := checkMailImage(client, url); err != nil {
		fmt.Fprintf(out, "::error::%s: %v\n", url, err)
		return 1
	}
	fmt.Fprintf(out, "ok %s\n", url)
	return 0
}

// runMailLogoCheckDefault checks accountmail.LogoURL.
func runMailLogoCheckDefault(client *http.Client, out io.Writer) int {
	return RunMailLogoCheck(client, accountmail.LogoURL, out)
}

func mailGet(client *http.Client, url string) (*http.Response, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mailRequestTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

func checkMailImage(client *http.Client, src string) error {
	if !strings.HasPrefix(src, "https://") {
		return fmt.Errorf("image %q is not an absolute https:// URL", src)
	}
	resp, cancel, err := mailGet(client, src)
	if err != nil {
		return fmt.Errorf("image %q: %w", src, err)
	}
	defer cancel()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("image %q answered %d, want 200", src, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "image/") {
		return fmt.Errorf("image %q answered content type %q, want image/*", src, ct)
	}
	return nil
}

func checkMailTemplate(client *http.Client, url string) error {
	resp, cancel, err := mailGet(client, url)
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("answered %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, templateMaxSize+1))
	if err != nil {
		return fmt.Errorf("reading the body: %w", err)
	}
	if len(body) > templateMaxSize {
		return fmt.Errorf("body exceeds GoTrue's template size limit of %d bytes", templateMaxSize)
	}
	tpl, err := template.New(url).Parse(string(body))
	if err != nil {
		return fmt.Errorf("template does not parse: %w", err)
	}

	registration := map[string]any{"display_name": "Ada Obi", "workspace_name": "Obi Partners"}
	variants := []map[string]any{
		{"Data": map[string]any{"registration": registration}},
		{"Data": map[string]any{}},
		{},
	}
	var rendered string
	for i, extra := range variants {
		data := map[string]any{
			"SiteURL":         "https://x.test",
			"ConfirmationURL": mailSampleConfirmationURL,
			"Email":           "ada@x.test",
			"Token":           "123456",
			"TokenHash":       "hash",
			"RedirectTo":      "https://x.test/",
		}
		for k, v := range extra {
			data[k] = v
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("template does not execute: %w", err)
		}
		if !strings.Contains(html.UnescapeString(buf.String()), mailSampleConfirmationURL) {
			return fmt.Errorf("rendered mail does not contain the ConfirmationURL")
		}
		if i == 0 {
			rendered = buf.String()
		}
	}

	seen := map[string]bool{}
	for _, m := range imgSrcRE.FindAllStringSubmatch(rendered, -1) {
		src := html.UnescapeString(m[1] + m[2])
		if seen[src] {
			continue
		}
		seen[src] = true
		if err := checkMailImage(client, src); err != nil {
			return err
		}
	}
	return nil
}
