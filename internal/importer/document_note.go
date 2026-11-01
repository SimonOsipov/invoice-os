package importer

import (
	"context"
	"errors"
)

// ImportDocumentNote files a read credit or debit note as a draft note on its original.
// Stub: answers wrongly until CRDDBTNOTE-05-04 lands.
func (s *Service) ImportDocumentNote(ctx context.Context, in DocumentImport) (BatchResult, error) {
	return BatchResult{}, errors.New("importer: ImportDocumentNote not implemented")
}

// notePositive stores a read amount positive. Stub: returns it unchanged.
func notePositive(v *string) (*string, error) { return v, nil }
