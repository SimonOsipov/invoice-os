package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Alert is one counted Dependabot alert.
type Alert struct {
	Number   int
	Package  string
	GHSA     string
	Severity string
	URL      string
	AgeDays  int
}

// Plain structs: a null object decodes to zero values, so no nil dereference.
type rawAlert struct {
	Number     int    `json:"number"`
	State      string `json:"state"`
	CreatedAt  string `json:"created_at"`
	HTMLURL    string `json:"html_url"`
	Dependency struct {
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
	} `json:"dependency"`
	SecurityAdvisory struct {
		GHSA     string `json:"ghsa_id"`
		Severity string `json:"severity"`
	} `json:"security_advisory"`
}

// Select returns the open alerts older than minAge at now, ascending by number.
// The cut is strict: an alert exactly minAge old does not count.
func Select(raw []byte, now time.Time, minAge time.Duration) ([]Alert, error) {
	var in []rawAlert
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("alerts json: %w", err)
	}
	var out []Alert
	for _, r := range in {
		if r.State != "open" {
			continue
		}
		created, err := time.Parse(time.RFC3339, r.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("alert #%d created_at %q: %w", r.Number, r.CreatedAt, err)
		}
		age := now.Sub(created)
		if age <= minAge {
			continue
		}
		sev := r.SecurityAdvisory.Severity
		if sev == "" {
			sev = "unknown"
		}
		out = append(out, Alert{
			Number:   r.Number,
			Package:  r.Dependency.Package.Name,
			GHSA:     r.SecurityAdvisory.GHSA,
			Severity: sev,
			URL:      r.HTMLURL,
			AgeDays:  int(age / (24 * time.Hour)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// Body renders the issue body for a non-empty alert list.
func Body(alerts []Alert, now time.Time, days int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "These Dependabot alerts are open more than %d days as of %s.\n\n", days, now.UTC().Format(time.RFC3339))
	b.WriteString("| Alert | Package | GHSA | Severity | Age (days) | Link |\n|---|---|---|---|---|---|\n")
	for _, a := range alerts {
		fmt.Fprintf(&b, "| #%d | %s | %s | %s | %d | %s |\n", a.Number, a.Package, a.GHSA, a.Severity, a.AgeDays, a.URL)
	}
	fmt.Fprintf(&b, "\nThis issue closes itself when no open alert is older than %d days.\n", days)
	return b.String()
}
