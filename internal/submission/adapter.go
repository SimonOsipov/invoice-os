package submission

import (
	"context"
)

// Adapter is the versioned seam between the submission worker and any route to the tax
// authority. Every method is single-shot: no internal retry, backoff or circuit breaking
// ([adapters-are-single-shot]) — retry is the River job's business.
//
// An adapter NEVER touches the database ([adapters-are-db-free]). It returns Evidence; the
// caller stamps job identity onto it and writes it through RecordExchange.
type Adapter interface {
	// Name is the stable adapter key, persisted as submission_jobs.adapter and
	// app_exchange.adapter (both NOT NULL, CHECK char_length > 0). It is also the registry key.
	Name() string

	// Version is the adapter's contract version, persisted as *.adapter_version
	// (both NOT NULL, CHECK char_length > 0).
	Version() string

	// Transform projects a canonical invoice onto this adapter's wire form. Pure: no I/O,
	// no clock, no mutation of c. On error it returns a zero Wire and the caller records a
	// "transform_failed" exchange with no headers and no bodies
	// ([transform-yields-no-evidence]).
	Transform(ctx context.Context, c Canonical) (Wire, error)

	// Submit sends w under idemKey. It returns exactly one Result variant plus the Evidence
	// of the attempt — including attempts that never reached the wire.
	Submit(ctx context.Context, w Wire, idemKey string) (Result, Evidence)

	// Poll resumes a deferred verdict from the opaque Ref a prior Pending carried.
	Poll(ctx context.Context, ref Ref) (Result, Evidence)
}

// Ref is an adapter-defined, opaque handle for a pending submission, round-tripped through
// submission_jobs.poll_ref. Never an idempotency key ([ref-is-a-named-string]).
type Ref string
