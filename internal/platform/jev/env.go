// env.go reads the client's on/off mode from the process environment.
package jev

import (
	"log/slog"
	"os"
	"time"
)

const (
	EnvKey  = "TYPESAFE_API_KEY"
	EnvFake = "JEV_FAKE"
)

// FromEnv never exits the process. The key is never trimmed, so a whitespace
// key counts as set.
func FromEnv(logger *slog.Logger) (*Client, error) {
	return newClient(config{
		key:       os.Getenv(EnvKey),
		endpoint:  endpoint,
		budget:    budget,
		retryWait: retryWait,
		now:       time.Now,
		sleep:     realSleep,
	}, logger), nil
}

// Enabled is false when no key is set and fake mode is off. Callers skip the
// check.
func (c *Client) Enabled() bool { return c.cfg.key != "" || c.cfg.fake }
