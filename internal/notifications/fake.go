package notifications

import "context"

// FakeHubSpot and FakeResend stand in for the vendors in fake mode: no network.
type FakeHubSpot struct{}

func (FakeHubSpot) Upsert(context.Context, Contact) error { return nil }

func (FakeHubSpot) OpenDemoDeal(context.Context, Contact, string) error { return nil }

func (FakeHubSpot) EraseContact(context.Context, string) error { return nil }

type FakeResend struct{}

func (FakeResend) Sync(context.Context, Contact, bool) error { return nil }

func (FakeResend) EraseContact(context.Context, string) error { return nil }

type FakeBlogResend struct{}

func (FakeBlogResend) SyncBlog(context.Context, BlogSubscriber) error { return nil }

func (FakeBlogResend) ContactUnsubscribed(context.Context, string) (bool, error) { return false, nil }
