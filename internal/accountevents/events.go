// Package accountevents moves the gateway's account events into per-workspace audit rows.
package accountevents

import "github.com/SimonOsipov/invoice-os/internal/audit"

// Events lists the six account event names.
var Events = audit.AccountEvents
