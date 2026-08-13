// Command forcepushgate fails a pull request whose branch was force-pushed after
// the PR was opened for review, unless a human has reviewed it again since.
//
// Why the gate is shaped this way. A force push on a draft nobody has read costs
// nothing: the two that reached this repo (PR #115 INVED-01 on 2026-07-29, PR #140
// BUG-04 on 2026-08-06) both landed hours before `ready_for_review`, and neither
// orphaned a review. A force push AFTER review is the damaging one — the commits a
// reviewer approved stop existing, and the approval now points at history no one
// read. So the gate asks about the review, not about the act.
//
// It clears itself. A fresh review after the rewrite means a human has looked at
// the new history, which is exactly the remedy — so the check goes green again
// without anyone editing a file or overriding a branch rule.
//
// GitHub's PR timeline is the only place this evidence exists. A force push leaves
// nothing in the local repository to find, and subagent shell commands are not
// written to the session transcript, so nothing on the machine records it either.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

type event struct {
	Event       string `json:"event"`
	CreatedAt   string `json:"created_at"`
	SubmittedAt string `json:"submitted_at"`
	State       string `json:"state"`
}

// when returns the event's timestamp. Reviews carry submitted_at; everything
// else carries created_at.
func (e event) when() (time.Time, bool) {
	for _, raw := range []string{e.SubmittedAt, e.CreatedAt} {
		if raw == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type pull struct {
	Number    int    `json:"number"`
	Draft     bool   `json:"draft"`
	CreatedAt string `json:"created_at"`
}

type verdict struct {
	OK     bool
	Reason string
}

// decide answers one question: has this branch been force-pushed since anyone
// could have reviewed it, without a review afterwards?
func decide(pr pull, timeline []event) verdict {
	// Floor. An empty timeline means the fetch failed or the query is wrong, and
	// "no force pushes found" would then be indistinguishable from a clean PR —
	// the exact way an absence check reports a false green.
	if len(timeline) == 0 {
		return verdict{false, "the PR timeline came back empty: the fetch is broken, not the PR clean"}
	}
	if pr.Draft {
		return verdict{true, "PR is a draft — nobody is reviewing it yet, so a rewrite costs nothing"}
	}

	readyAt, err := time.Parse(time.RFC3339, pr.CreatedAt)
	if err != nil {
		return verdict{false, fmt.Sprintf("cannot read the PR's created_at (%q): %v", pr.CreatedAt, err)}
	}
	// A PR opened as a draft becomes reviewable at ready_for_review; one opened
	// ready has been reviewable since it was created.
	for _, e := range timeline {
		if e.Event != "ready_for_review" {
			continue
		}
		if t, ok := e.when(); ok && t.After(readyAt) {
			readyAt = t
		}
	}

	var lastRewrite time.Time
	for _, e := range timeline {
		if e.Event != "head_ref_force_pushed" {
			continue
		}
		if t, ok := e.when(); ok && !t.Before(readyAt) && t.After(lastRewrite) {
			lastRewrite = t
		}
	}
	if lastRewrite.IsZero() {
		return verdict{true, "no force push since the PR became reviewable"}
	}

	for _, e := range timeline {
		if e.Event != "reviewed" {
			continue
		}
		if t, ok := e.when(); ok && t.After(lastRewrite) {
			return verdict{true, fmt.Sprintf(
				"force-pushed at %s, but reviewed again at %s",
				lastRewrite.Format(time.RFC3339), t.Format(time.RFC3339))}
		}
	}

	return verdict{false, fmt.Sprintf(
		"the branch was force-pushed at %s, after the PR became reviewable at %s, and nobody has "+
			"reviewed it since. The commits a reviewer read may no longer exist. Ask for a fresh "+
			"review — that clears this check on its own.",
		lastRewrite.Format(time.RFC3339), readyAt.Format(time.RFC3339))}
}

func load(path string, into any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func main() {
	prPath := flag.String("pr", "", "path to the PR JSON (gh api repos/O/R/pulls/N)")
	timelinePath := flag.String("timeline", "", "path to the timeline JSON array")
	flag.Parse()

	if *prPath == "" || *timelinePath == "" {
		fmt.Fprintln(os.Stderr, "both -pr and -timeline are required")
		os.Exit(2)
	}

	var pr pull
	if err := load(*prPath, &pr); err != nil {
		fmt.Fprintf(os.Stderr, "::error::cannot read %s: %v\n", *prPath, err)
		os.Exit(1)
	}
	var timeline []event
	if err := load(*timelinePath, &timeline); err != nil {
		fmt.Fprintf(os.Stderr, "::error::cannot read %s: %v\n", *timelinePath, err)
		os.Exit(1)
	}

	v := decide(pr, timeline)
	if !v.OK {
		fmt.Printf("::error::PR #%d: %s\n", pr.Number, v.Reason)
		os.Exit(1)
	}
	fmt.Printf("PR #%d: %s (%d timeline events)\n", pr.Number, v.Reason, len(timeline))
}
