package notifications

import "context"

// FakeHubSpot and FakeResend stand in for the vendors in fake mode: no network.
type FakeHubSpot struct{}

func (FakeHubSpot) Upsert(context.Context, Contact) error { return nil }

func (FakeHubSpot) OpenDemoDeal(context.Context, Contact, string) error { return nil }

func (FakeHubSpot) EraseContact(context.Context, string) error { return nil }

// FakeResend returns State as the contact state, so a test drives the erase rebuild.
type FakeResend struct{ State *ResendState }

func (FakeResend) Sync(context.Context, Contact, bool) error { return nil }

func (FakeResend) EraseContact(context.Context, string) error { return nil }

func (f FakeResend) ContactState(context.Context, string) (ResendState, error) {
	if f.State == nil {
		return ResendState{}, nil
	}
	return *f.State, nil
}

func (FakeResend) RestoreContact(context.Context, string, string, string, ResendState) error {
	return nil
}

type FakeBlogResend struct{}

func (FakeBlogResend) SyncBlog(context.Context, BlogSubscriber) error { return nil }

func (FakeBlogResend) ContactUnsubscribed(context.Context, string) (bool, error) { return false, nil }
