// Package accountevents moves the gateway's account events into per-workspace audit rows.
package accountevents

// Events lists the six account event names; the outbox CHECK and the gateway constants match (TestEvents_TheThreeListsAgree).
var Events = []string{
	"account.signed_in",
	"account.sign_in_failed",
	"account.sign_in_blocked",
	"account.signed_out",
	"account.password_reset_requested",
	"account.password_reset_completed",
}
