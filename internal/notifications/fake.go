package notifications

import "context"

// FakeHubSpot and FakeResend stand in for the vendors in fake mode: no network.
type FakeHubSpot struct{}

func (FakeHubSpot) Upsert(context.Context, Contact) error { return nil }

func (FakeHubSpot) OpenDemoDeal(context.Context, Contact, string) error { return nil }

type FakeResend struct{}

func (FakeResend) Sync(context.Context, Contact, bool) error { return nil }
