package notifications

// STUB (AUTH-17-03 red): compile-only; the executor replaces it.

import "context"

type FakeHubSpot struct{}

func (FakeHubSpot) Upsert(context.Context, Contact) error { return nil }

type FakeResend struct{}

func (FakeResend) Sync(context.Context, Contact, bool) error { return nil }
