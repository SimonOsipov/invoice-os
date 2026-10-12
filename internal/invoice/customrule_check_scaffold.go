// QA scaffold for ENGI-14-04 (Mode A). Execution deletes this file and writes
// Validator.CheckCustomRule in validator.go.
package invoice

import (
	"context"
	"errors"

	"github.com/SimonOsipov/invoice-os/internal/customrule"
)

var errNotImplemented = errors.New("invoice: not implemented")

func (v *Validator) CheckCustomRule(ctx context.Context, in customrule.CheckInput) error {
	return errNotImplemented
}
