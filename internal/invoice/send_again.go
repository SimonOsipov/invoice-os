package invoice

import (
	"context"
	"errors"
)

// Stubs for RETRY-01-01 Mode A: the executor replaces this file's bodies.
var (
	ErrNotResendable = errors.New("invoice: not resendable")
	ErrResendBusy    = errors.New("invoice: a submission job is already running")
)

func (s *Submitter) SendAgain(ctx context.Context, id string) (Invoice, error) {
	return Invoice{}, errors.New("invoice: SendAgain not implemented")
}
