// queue_test.go: the client-level configuration surface, pinned by field set.
package queue_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// A client-level job timeout would raise River's 60s default for every worker on the client,
// not just the one that needs it: extraction.ExtractWorker raises its own through Timeout(),
// which the executor resolves as cmp.Or(workUnit.Timeout(), clientJobTimeout).
func TestQueueConfig_HasNoJobTimeoutField(t *testing.T) {
	want := []string{"Queues", "Workers", "RetryPolicy", "Logger"}

	rt := reflect.TypeOf(queue.Config{})
	got := make([]string, 0, rt.NumField())
	for i := range rt.NumField() {
		got = append(got, rt.Field(i).Name)
	}
	if len(got) == 0 {
		t.Fatal("queue.Config declares no fields; the assertions below would pass vacuously")
	}

	// Named separately from the set check below so the failure says why, not just what.
	for _, name := range got {
		if strings.Contains(strings.ToLower(name), "timeout") {
			t.Errorf("queue.Config carries %q: a client-level timeout applies to SubmitWorker and PollWorker too, and the per-worker Timeout() the extraction worker already ships makes it unnecessary", name)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("queue.Config declares %v, want exactly %v", got, want)
	}
}
