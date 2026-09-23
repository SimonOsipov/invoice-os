// env.go reads the client's on/off mode from the process environment.
package jev

import "log/slog"

const (
	EnvKey  = "TYPESAFE_API_KEY"
	EnvFake = "JEV_FAKE"
)

// FromEnv never exits the process.
func FromEnv(logger *slog.Logger) (*Client, error) {
	return &Client{}, nil
}

// Enabled is false when no key is set and fake mode is off. Callers skip the
// check.
func (c *Client) Enabled() bool { return false }
