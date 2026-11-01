// Command nightlycontacts proves a contact reaches HubSpot and Resend, deletes the run's contacts,
// and writes the hubspot section for nightlysummary. Phases: start, check, finish.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/notifications"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, notifications.RealTransport(), os.Stdout, os.Stderr))
}

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// run returns 0 on green, 1 on a failed step, 2 on a wrong call (no vendor call, no file).
func run(args []string, getenv func(string) string, transport http.RoundTripper, stdout, stderr io.Writer) int {
	return runWith(args, getenv, transport, time.Sleep, stdout, stderr)
}

func runWith(args []string, getenv func(string) string, transport http.RoundTripper, sleep func(time.Duration), stdout, stderr io.Writer) int {
	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "nightlycontacts: "+format+"\n", a...)
		return 2
	}
	if len(args) == 0 {
		return usage("usage: nightlycontacts start|check|finish -out <file> [-in <dir> -started <RFC3339>]")
	}
	phase := args[0]
	fs := flag.NewFlagSet("nightlycontacts "+phase, flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "section file to write (required)")
	in := fs.String("in", "", "finish: directory holding start.json and check.json")
	startedFlag := fs.String("started", "", "finish: run start as RFC3339")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *out == "" || fs.NArg() != 0 {
		return usage("-out is required")
	}
	need := map[string][]string{
		"start":  {"HUBSPOT_TOKEN", "RESEND_API_KEY", "HUBSPOT_DEAL_STAGE"},
		"check":  {"HUBSPOT_TOKEN", "RESEND_API_KEY", "HUBSPOT_DEAL_STAGE", "GATEWAY_URL", "NIGHTLY_RUN_ID"},
		"finish": {"HUBSPOT_TOKEN", "RESEND_API_KEY"},
	}[phase]
	if need == nil {
		return usage("unknown phase %q", phase)
	}
	for _, name := range need {
		if getenv(name) == "" {
			return usage("%s is not set", name)
		}
	}
	if phase == "check" && !runIDPattern.MatchString(getenv("NIGHTLY_RUN_ID")) {
		return usage("NIGHTLY_RUN_ID is not a run id")
	}
	var started time.Time
	if phase == "finish" {
		if *in == "" {
			return usage("finish needs -in")
		}
		var err error
		if started, err = time.Parse(time.RFC3339, *startedFlag); err != nil {
			return usage("finish needs -started as RFC3339")
		}
	}

	hc := notifications.NewHTTPClient(transport)
	keys := notifications.Keys{HubSpotToken: getenv("HUBSPOT_TOKEN"), ResendAPIKey: getenv("RESEND_API_KEY")}
	r := &runner{
		v: &vendors{
			hc: hc, hsToken: keys.HubSpotToken, rsKey: keys.ResendAPIKey, sleep: sleep,
			hsErase: notifications.NewHubSpot(notifications.HubSpotBaseURL, keys, hc),
			rsErase: notifications.NewResend(notifications.ResendBaseURL, keys, hc),
		},
		domain: getenv("RECEIVING_DOMAIN"), stage: getenv("HUBSPOT_DEAL_STAGE"),
		gateway: getenv("GATEWAY_URL"), runID: getenv("NIGHTLY_RUN_ID"),
	}
	var steps []stepResult
	switch phase {
	case "start":
		steps = r.start()
	case "check":
		steps = r.check()
	default:
		steps = r.finish(*in, started)
	}
	return writeSection(*out, steps, stdout, stderr)
}
