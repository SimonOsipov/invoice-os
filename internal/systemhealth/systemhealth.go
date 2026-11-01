// Package systemhealth samples the fleet roll-up and job counts and serves them to staff.
package systemhealth

import (
	"slices"
	"strings"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	SampleInterval = 5 * time.Minute
	TrendWindow    = 7 * 24 * time.Hour // ceiling: ~600 KB uncompressed at 2,016 samples; aggregate per hour or compress the route if the window grows
	FleetTimeout   = 40 * time.Second   // above the gateway's 30 s healthz plus 3 s readyz probe
	FleetUnknown   = "unknown"
)

var (
	JobStates   = []string{"queued", "submitting", "pending", "accepted", "rejected", "failed", "dead_lettered", "cancelled"}
	QueueStates = []string{"queued", "submitting", "pending"}
)

var RiverQueueStates = []string{"available", "scheduled", "retryable", "running"}

const RiverDeadState = "discarded" // River deletes such a row 7 days after the discard

// ceiling: hand-kept map of job kind to owning service
var JobOwners = map[string]string{
	"submission_submit": "submission", "submission_poll": "submission", "extraction_extract": "submission",
	"contact_deliver": "notifications", "demo_deal": "notifications", "blog_subscriber_sync": "notifications", "registrant_intake": "notifications",
}

const JobRunner = "worker" // runs every queue, so its view is the total

// DeadFromSubmissionJobs are the kinds whose dead letters are submission_jobs rows in dead_lettered.
var DeadFromSubmissionJobs = []string{"submission_submit", "submission_poll"}

// ceiling: hand-kept list of the Cloud Run fleet; add a new service here or it is not selectable
var MetricServices = []string{"app", "auth", "blog", "dashboard", "docling", "gateway", "invoice", "landing", "library", "notifications", "ops-console", "portfolio", "submission", "support-console", "tenancy", "validation", "worker"}

// Counts returns a copy of raw with every JobStates entry present.
func Counts(raw map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(raw)+len(JobStates))
	for k, v := range raw {
		out[k] = v
	}
	for _, s := range JobStates {
		if _, ok := out[s]; !ok {
			out[s] = 0
		}
	}
	return out
}

func QueueDepth(c map[string]int64) int64 {
	var n int64
	for _, s := range QueueStates {
		n += c[s]
	}
	return n
}

// PreviewSuffix is "-pr-<N>" on a PR preview, "-nightly" on the nightly, else "".
func PreviewSuffix(deployEnv string) string {
	switch platform.Posture(deployEnv) {
	case platform.PostureNightly:
		return "-nightly"
	case platform.PosturePreview:
	default:
		return ""
	}
	return "-" + deployEnv[strings.LastIndex(deployEnv, "pr-"):]
}

// JobTotals is queue depth and dead-lettered over a set of River kinds.
type JobTotals struct {
	QueueDepth   int64 `json:"queue_depth"`
	DeadLettered int64 `json:"dead_lettered"`
}

// JobView is the total over every kind and the totals per owning service.
type JobView struct {
	Total    JobTotals            `json:"total"`
	Services map[string]JobTotals `json:"services"`
}

// JobViewOf folds {kind: {state: n}} into per-service totals; submissionDead is the submission_jobs dead_lettered count.
func JobViewOf(kinds map[string]map[string]int64, submissionDead int64) JobView {
	v := JobView{Services: map[string]JobTotals{JobRunner: {}, "submission": {}, "notifications": {}}}
	for _, owner := range JobOwners {
		v.Services[owner] = JobTotals{}
	}
	add := func(kind string, t JobTotals) {
		v.Total.QueueDepth += t.QueueDepth
		v.Total.DeadLettered += t.DeadLettered
		for _, key := range []string{JobRunner, JobOwners[kind]} {
			if key == "" {
				continue
			}
			s := v.Services[key]
			s.QueueDepth += t.QueueDepth
			s.DeadLettered += t.DeadLettered
			v.Services[key] = s
		}
	}
	for kind, states := range kinds {
		var t JobTotals
		for _, st := range RiverQueueStates {
			t.QueueDepth += states[st]
		}
		if !slices.Contains(DeadFromSubmissionJobs, kind) {
			t.DeadLettered = states[RiverDeadState]
		}
		add(kind, t)
	}
	add("submission_jobs", JobTotals{DeadLettered: submissionDead})
	s := v.Services["submission"]
	s.DeadLettered += submissionDead
	v.Services["submission"] = s
	return v
}
