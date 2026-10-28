package tenancy

import (
	"context"
	"errors"
)

var (
	ErrRoleChangeNotPermitted = errors.New("tenancy: role change not permitted")
	ErrInvalidRole            = errors.New("tenancy: invalid role")
)

var errRoleNotImplemented = errors.New("tenancy: role change not implemented")

// Stub for ACCESS-01-01 Test-Spec: the red tests compile against it.
func (s *Store) SetMemberRole(ctx context.Context, userID, role string) (MemberAccess, error) {
	return MemberAccess{}, errRoleNotImplemented
}
