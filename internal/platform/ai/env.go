// env.go reads the client's on/off/fake mode from the process environment.
package ai

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

const (
	EnvKey  = "OPENROUTER_API_KEY"
	EnvFake = "AI_FAKE"
)

// ErrOff marks a call on a client with no key and no fake mode.
var ErrOff = errors.New("ai: off")

// parseFake reads AI_FAKE. Unset and empty are false; anything else must
// parse, because the permissive state is "not fake" and a typo would reopen
// real calls.
func parseFake(raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	return strconv.ParseBool(raw)
}

// FromEnv never exits the process. Its only error is an unparseable AI_FAKE.
func FromEnv(logger *slog.Logger) (*Client, error) {
	fake, err := parseFake(os.Getenv(EnvFake))
	if err != nil {
		return nil, fmt.Errorf("ai: %s: %v", EnvFake, err)
	}
	return newClient(config{
		key:      os.Getenv(EnvKey),
		endpoint: endpoint,
		fake:     fake,
		budget:   budget,
		now:      time.Now,
		sleep:    realSleep,
	}, logger), nil
}

// Enabled is false when no key is set and fake mode is off. Callers skip the
// step.
func (c *Client) Enabled() bool { return c.cfg.key != "" || c.cfg.fake }
