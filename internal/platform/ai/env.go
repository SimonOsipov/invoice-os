// env.go reads the client's on/off/fake mode from the process environment.
// Stage 3 (AIR-02-02) fills in the bodies below; today they are stubs so the
// package compiles ahead of the red tests in env_test.go.
package ai

import (
	"errors"
	"log/slog"
)

const (
	EnvKey  = "OPENROUTER_API_KEY"
	EnvFake = "AI_FAKE"
)

// ErrOff marks a call on a client with no key and no fake mode.
var ErrOff = errors.New("ai: off")

// parseFake reads AI_FAKE. Stub: Stage 3 fills this in.
func parseFake(raw string) (bool, error) {
	return false, errors.New("ai: not implemented")
}

// FromEnv never exits the process; its only error is an unparseable AI_FAKE.
// Stub: Stage 3 fills this in.
func FromEnv(logger *slog.Logger) (*Client, error) {
	return nil, errors.New("ai: not implemented")
}

// Enabled is false when no key is set and fake mode is off. Stub: Stage 3
// fills this in.
func (c *Client) Enabled() bool {
	return false
}
