package approval

import (
	"context"
	"errors"
)

// The policy handler seam, declared beside the methods that satisfy it (the
// store.go:33-47 shape). Only the three this subtask implements: a declared-but-
// unasserted function type is dead code no vet catches, and the draft/publish/delete
// signatures belong to the subtasks that build them.
type (
	PolicyLister  func(ctx context.Context) ([]Policy, error)
	PolicyGetter  func(ctx context.Context, id string) (Policy, error)
	PolicyCreator func(ctx context.Context, name, scope string) (Policy, error)
)

var (
	_ PolicyLister  = new(Store).ListPolicies
	_ PolicyGetter  = new(Store).GetPolicy
	_ PolicyCreator = new(Store).CreatePolicy
)

// errPolicyStoreStub marks the three methods below as declarations only — the specs in
// policy_crud_test.go were written first and fail against it. Returned rather than
// panicked so each spec reports its own failure and every t.Cleanup still runs; delete
// it with the last stub body.
var errPolicyStoreStub = errors.New("approval: policy store not implemented")

// ListPolicies returns the tenant's live policies, each with its version list and the
// highest version's step tree.
func (s *Store) ListPolicies(ctx context.Context) ([]Policy, error) {
	return nil, errPolicyStoreStub
}

// GetPolicy returns one live policy, or ErrPolicyNotFound.
func (s *Store) GetPolicy(ctx context.Context, id string) (Policy, error) {
	return Policy{}, errPolicyStoreStub
}

// CreatePolicy inserts a policy and its empty draft version 1.
func (s *Store) CreatePolicy(ctx context.Context, name, scope string) (Policy, error) {
	return Policy{}, errPolicyStoreStub
}
