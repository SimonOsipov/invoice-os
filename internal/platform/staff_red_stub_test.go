package platform

import (
	"context"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// RED STUB, delete with this file once auth.StaffFromContext exists (ENGI-11-05):
// add `func staffCaller(ctx context.Context) (auth.Identity, bool) { return auth.StaffFromContext(ctx) }`
// to staff_class_test.go. Cross-package symbols cannot be stubbed from a test file.
func staffCaller(context.Context) (auth.Identity, bool) { return auth.Identity{}, false }
