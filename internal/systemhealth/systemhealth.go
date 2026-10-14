// Package systemhealth samples the fleet roll-up and job counts and serves them to staff.
package systemhealth

import "time"

const (
	SampleInterval = 5 * time.Minute
	TrendWindow    = 7 * 24 * time.Hour
	FleetTimeout   = 15 * time.Second
	FleetUnknown   = "unknown"
)

var (
	JobStates   = []string{"queued", "submitting", "pending", "accepted", "rejected", "failed", "dead_lettered", "cancelled"}
	QueueStates = []string{"queued", "submitting", "pending"}
)

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
