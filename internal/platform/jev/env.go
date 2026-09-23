// env.go reads the client's on/off/fake mode from the process environment.
package jev

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

const (
	EnvKey  = "TYPESAFE_API_KEY"
	EnvFake = "JEV_FAKE"
)

// FromEnv never exits the process. Neither variable is trimmed: a whitespace
// key counts as set, and a padded JEV_FAKE is an error.
func FromEnv(logger *slog.Logger) (*Client, error) {
	var fake bool
	if raw := os.Getenv(EnvFake); raw != "" {
		var err error
		if fake, err = strconv.ParseBool(raw); err != nil {
			return nil, fmt.Errorf("jev: %s: %v", EnvFake, err)
		}
	}
	key := os.Getenv(EnvKey)
	// Unlike AI_FAKE, fake mode never overrides a key (TestFromEnv_FakeWithAKeyRefusesToStart).
	if fake && key != "" {
		return nil, fmt.Errorf("jev: %s is true and %s is set; unset one", EnvFake, EnvKey)
	}
	return newClient(config{
		key:       key,
		endpoint:  endpoint,
		fake:      fake,
		budget:    budget,
		retryWait: retryWait,
		now:       time.Now,
		sleep:     realSleep,
	}, logger), nil
}

// Enabled is false when no key is set and fake mode is off. Callers skip the
// check.
func (c *Client) Enabled() bool { return c.cfg.key != "" || c.cfg.fake }
