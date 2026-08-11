package approval

import (
	"log/slog"
	"net/http"
)

// STUB — the six approval-policy handlers are not implemented yet. The factories
// exist so policy_handlers_test.go compiles and fails on its assertions rather than
// on a missing symbol. They answer the JSON error shape, not http.Error's
// text/plain, so a failing test reports a status mismatch and not a decode error.

func notImplementedPolicyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}

func ListPoliciesHandler(list PolicyLister, log *slog.Logger) http.HandlerFunc {
	return notImplementedPolicyHandler()
}

func GetPolicyHandler(get PolicyGetter, log *slog.Logger) http.HandlerFunc {
	return notImplementedPolicyHandler()
}

func CreatePolicyHandler(create PolicyCreator, log *slog.Logger) http.HandlerFunc {
	return notImplementedPolicyHandler()
}

func PutDraftHandler(put PolicyDrafter, log *slog.Logger) http.HandlerFunc {
	return notImplementedPolicyHandler()
}

func PublishPolicyHandler(publish PolicyPublisher, log *slog.Logger) http.HandlerFunc {
	return notImplementedPolicyHandler()
}

func DeletePolicyHandler(del PolicyDeleter, log *slog.Logger) http.HandlerFunc {
	return notImplementedPolicyHandler()
}
