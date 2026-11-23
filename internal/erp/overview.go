package erp

import "context"

// Red-test stubs (ERPF-08-01): the types and reads are replaced by the implementation.

type Overview struct{}

func (s *Store) Overview(ctx context.Context, entityID, connectionID string) (Overview, error) {
	return Overview{}, nil
}

func (s *Store) Marks(ctx context.Context, entityID, invoiceID string) ([]DocumentMark, error) {
	return nil, nil
}
