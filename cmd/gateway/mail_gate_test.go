package main

import (
	"regexp"
	"strings"
	"testing"
)

var (
	mailTemplatesRunRE = regexp.MustCompile(`railway-env\.sh check-mail-templates\b`)
	mailLogoRunRE      = regexp.MustCompile(`tools/prenv mail-logo-check\b`)
	waitAuthRunRE      = regexp.MustCompile(`railway-env\.sh wait-deployment auth\b`)
)

// A step gated to the wrong event runs against a token it lacks (dispatch) or never runs on push;
// no runtime row sees an if: expression (AC 8, AC 10).
func TestFleetGateMailStepsRunOnTheirEvents(t *testing.T) {
	steps := jobSteps(jobBlock(devEnvCode(t), "fleet-gate"))
	wait := stepsRunning(steps, waitAuthRunRE)
	if len(steps) == 0 || len(wait) != 1 {
		t.Fatalf("fleet-gate parsed to %d step(s), %d running wait-deployment auth; the scan is broken", len(steps), len(wait))
	}

	tpl := stepsRunning(steps, mailTemplatesRunRE)
	if len(tpl) != 1 {
		t.Fatalf("fleet-gate has %d step(s) running check-mail-templates, want 1", len(tpl))
	}
	step := steps[tpl[0]]
	if g, want := stepIf(step), "github.event_name != 'workflow_dispatch'"; g != want {
		t.Errorf("the template step's if: reads %q, want %q (dispatch carries only the project token)", g, want)
	}
	if name, _ := stepKey(step, "name"); name != "Gate on the account-mail templates" {
		t.Errorf("the template step is named %q, want %q", name, "Gate on the account-mail templates")
	}
	if tpl[0] <= wait[0] {
		t.Errorf("the template step is step %d, want it after the auth deployment wait (step %d)", tpl[0], wait[0])
	}
	if got := stepEnv(step)["RAILWAY_API_TOKEN"]; got != "${{ secrets.RAILWAY_API_TOKEN }}" {
		t.Errorf("the template step's RAILWAY_API_TOKEN = %q, want the account secret", got)
	}
	if !strings.Contains(runText(step), "needs.prepare-env.outputs.environment_id") {
		t.Errorf("the template step does not pass needs.prepare-env.outputs.environment_id:\n%s", runText(step))
	}

	goSetup := -1
	for i, s := range steps {
		if v, _ := stepKey(s, "uses"); v == "actions/setup-go@v5" {
			goSetup = i
			text := strings.Join(s, "\n")
			for _, want := range []string{"go-version-file: go.mod", "cache: false"} {
				if !strings.Contains(text, want) {
					t.Errorf("fleet-gate's setup-go step lacks %q", want)
				}
			}
		}
	}
	if goSetup < 0 || goSetup >= tpl[0] {
		t.Errorf("fleet-gate's setup-go step is at %d, want it before the template step (%d)", goSetup, tpl[0])
	}

	logo := stepsRunning(steps, mailLogoRunRE)
	if len(logo) != 1 {
		t.Fatalf("fleet-gate has %d step(s) running mail-logo-check, want 1", len(logo))
	}
	lstep := steps[logo[0]]
	if g, want := stepIf(lstep), "github.event_name == 'push'"; g != want {
		t.Errorf("the logo step's if: reads %q, want %q (production serves the logo only after a push deploy)", g, want)
	}
	if name, _ := stepKey(lstep, "name"); name != "Gate on the account-mail logo" {
		t.Errorf("the logo step is named %q, want %q", name, "Gate on the account-mail logo")
	}
	if !strings.Contains(runText(lstep), "go run ./tools/prenv mail-logo-check") {
		t.Errorf("the logo step runs %q, want `go run ./tools/prenv mail-logo-check`", runText(lstep))
	}
}
