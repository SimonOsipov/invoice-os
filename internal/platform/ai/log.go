// log.go: the one log line per call. Stub for Stage 2.5 -- Stage 3 wires it
// into Call and fills the body.
package ai

import (
	"context"
	"time"
)

// logCall will write the one line per call (AIR-02-03).
func (c *Client) logCall(ctx context.Context, req Request, r result, latency time.Duration) {}
