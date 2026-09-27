// Package liveprobe holds the gated pre-merge probe: one real Jev call per
// question type through OpenRouter.
package liveprobe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// zdrURL is public; the probe sends no key to it.
const zdrURL = "https://openrouter.ai/api/v1/endpoints/zdr"

// lpLine is one parsed "jev call" log line plus the question type asked.
type lpLine struct {
	Type      jev.QuestionType `json:"-"`
	Purpose   string           `json:"purpose"`
	Model     string           `json:"model"`
	Outcome   string           `json:"outcome"`
	Attempts  int              `json:"attempts"`
	Cost      float64          `json:"cost"`
	LatencyMS int64            `json:"latency_ms"`
}

// Synthetic text only: no customer invoice leaves the repo.
const lpInvoice = `INVOICE No. INV-2026-0042
Date: 2026-09-01
Seller: Example Supplies Ltd, 12 Sample Road, Lagos. TIN 00000000-0001
Buyer: Demo Trading Co, 3 Test Avenue, Abuja. TIN 00000000-0002
1 x Office chair @ 50,000.00 NGN = 50,000.00 NGN
2 x Desk lamp @ 25,000.00 NGN = 50,000.00 NGN
Subtotal: 100,000.00 NGN
VAT 7.5%: 7,500.00 NGN
Total due: 107,500.00 NGN`

func lpRequests() []jev.Request {
	return []jev.Request{
		{Purpose: jev.PurposeValueCheck, State: lpInvoice, Questions: map[string]jev.Question{
			"total_ok": {
				Type:         jev.TypeNoul,
				Instructions: "Is the invoice total due 107,500.00 NGN?",
				True:         "The document states a total due of 107,500.00 NGN.",
				False:        "The document states a different total due, or none.",
			},
		}},
		{Purpose: jev.PurposeDocumentType, State: lpInvoice, Questions: map[string]jev.Question{
			"doc_type": {
				Type:         jev.TypeChoice,
				Instructions: "What kind of document is this?",
				Options: []jev.Option{
					{Name: "invoice", Description: "A bill requesting payment for goods or services."},
					{Name: "credit_note", Description: "A document reducing an amount previously invoiced."},
					{Name: "other", Description: "Any other document."},
				},
				Default: "invoice",
			},
		}},
		{Purpose: jev.PurposeMappingCheck, State: lpInvoice, Questions: map[string]jev.Question{
			"buyer_tin": {
				Type:         jev.TypeScore,
				Instructions: "How well does the value 00000000-0002 fit the field buyer TIN?",
				Options: []jev.Option{
					{Name: "poor", Description: "The value does not fit the field."},
					{Name: "partial", Description: "The value may fit the field."},
					{Name: "good", Description: "The value clearly fits the field."},
				},
				Default: "good",
			},
		}},
	}
}

// lpGate never puts the key value in reason.
func lpGate() (open bool, reason string) {
	var missing []string
	if os.Getenv(jev.EnvKey) == "" {
		missing = append(missing, jev.EnvKey+" is unset or empty")
	}
	if os.Getenv("JEV_PROBE") != "1" {
		missing = append(missing, `JEV_PROBE is not "1"`)
	}
	if len(missing) > 0 {
		return false, "live probe declined: " + strings.Join(missing, "; ")
	}
	return true, ""
}

// lpRun asks each request once and returns its parsed "jev call" line.
func lpRun(t *testing.T, build func(*slog.Logger) (*jev.Client, error)) []lpLine {
	t.Helper()
	var buf bytes.Buffer
	c, err := build(slog.New(slog.NewJSONHandler(&buf, nil)))
	if err != nil {
		t.Fatalf("build the Jev client: %v", err)
	}
	var lines []lpLine
	for _, req := range lpRequests() {
		buf.Reset()
		// The outcome on the line is the verdict's input; Ask's error adds nothing.
		_, _ = c.Ask(t.Context(), req)
		var line lpLine
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
			t.Fatalf("parse the %s jev call line %q: %v", req.Purpose, buf.Bytes(), err)
		}
		for _, q := range req.Questions {
			line.Type = q.Type
		}
		lines = append(lines, line)
	}
	return lines
}

// lpZDRVersions returns the versions named after " | " in each TypeSafe entry.
func lpZDRVersions(body []byte) (map[string]bool, error) {
	var resp struct {
		Data []struct {
			Name    string `json:"name"`
			ModelID string `json:"model_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode the ZDR list: %w", err)
	}
	out := map[string]bool{}
	for _, e := range resp.Data {
		if !strings.HasPrefix(e.ModelID, "typesafe/") {
			continue
		}
		if _, version, ok := strings.Cut(e.Name, " | "); ok {
			out[version] = true
		}
	}
	return out, nil
}

// lpVerdict returns the report even on failure, so a failing line still prints.
func lpVerdict(line lpLine, zdr map[string]bool) (report string, err error) {
	onZDR := zdr[line.Model]
	report = fmt.Sprintf("probe %s: outcome=%s model=%s on_zdr_list=%t latency_ms=%d attempts=%d cost=%s",
		line.Type, line.Outcome, line.Model, onZDR, line.LatencyMS, line.Attempts,
		strconv.FormatFloat(line.Cost, 'g', -1, 64))

	var errs []error
	if line.Outcome != "ok" {
		errs = append(errs, fmt.Errorf("outcome %q, want ok", line.Outcome))
	}
	if line.Attempts != 1 {
		errs = append(errs, fmt.Errorf("attempts %d, want 1", line.Attempts))
	}
	if !(line.Cost > 0) {
		errs = append(errs, fmt.Errorf("cost %v, want > 0", line.Cost))
	}
	if !strings.HasPrefix(line.Model, "typesafe/jev-") {
		errs = append(errs, fmt.Errorf("model %q, want a typesafe/jev- version", line.Model))
	}
	if !onZDR {
		errs = append(errs, fmt.Errorf("%q is not on the public ZDR list", line.Model))
	}
	return report, errors.Join(errs...)
}

func lpFetchZDR(t *testing.T) map[string]bool {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, zdrURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", zdrURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, read err %v", zdrURL, resp.StatusCode, err)
	}
	zdr, err := lpZDRVersions(body)
	if err != nil {
		t.Fatal(err)
	}
	return zdr
}

// Closed gate logs and returns, never t.Skip: CI shows a pass with the decline reason.
func TestJevLiveProbe(t *testing.T) {
	open, reason := lpGate()
	if !open {
		t.Log(reason)
		return
	}
	zdr := lpFetchZDR(t)
	for _, line := range lpRun(t, jev.FromEnv) {
		report, err := lpVerdict(line, zdr)
		t.Log(report)
		if err != nil {
			t.Errorf("probe %s failed: %v", line.Type, err)
		}
	}
}
