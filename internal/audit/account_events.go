package audit

// AccountEvents are the six account event names. TestAccountEvents_ListMatchesTheGateway
// pins them to the gateway constants; accountevents imports this package, so it cannot be the source.
var AccountEvents = []string{
	"account.signed_in",
	"account.sign_in_failed",
	"account.sign_in_blocked",
	"account.signed_out",
	"account.password_reset_requested",
	"account.password_reset_completed",
}
