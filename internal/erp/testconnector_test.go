package erp

import (
	"context"
	"testing"
)

// testFake is the one place these tests build the fake.
func testFake(t *testing.T) *Fake {
	t.Helper()
	return NewFake(testRedirect)
}

// scriptConnector overrides ListChanged, Fetch and FetchParty; a nil script delegates to the
// embedded connector, and every other method is inherited.
type scriptConnector struct {
	Connector
	list       func(ctx context.Context, c Credentials, scope string, cursor []byte) (Changes, error)
	fetch      func(ctx context.Context, c Credentials, ref DocRef) (Document, error)
	fetchParty func(ctx context.Context, c Credentials, scope, partyID string) (Party, error)
}

func (s scriptConnector) ListChanged(ctx context.Context, c Credentials, scope string, cursor []byte) (Changes, error) {
	if s.list != nil {
		return s.list(ctx, c, scope, cursor)
	}
	return s.Connector.ListChanged(ctx, c, scope, cursor)
}

func (s scriptConnector) Fetch(ctx context.Context, c Credentials, ref DocRef) (Document, error) {
	if s.fetch != nil {
		return s.fetch(ctx, c, ref)
	}
	return s.Connector.Fetch(ctx, c, ref)
}

func (s scriptConnector) FetchParty(ctx context.Context, c Credentials, scope, partyID string) (Party, error) {
	if s.fetchParty != nil {
		return s.fetchParty(ctx, c, scope, partyID)
	}
	return s.Connector.FetchParty(ctx, c, scope, partyID)
}
