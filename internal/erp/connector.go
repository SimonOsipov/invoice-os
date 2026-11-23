// Package erp is the seam to ERP vendors. A Connector speaks one vendor's API and returns
// raw vendor bytes; mapping, storage and scheduling live outside it.
package erp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// GrantKind names how a vendor grant authenticates.
type GrantKind string

const (
	GrantOAuthRefresh      GrantKind = "oauth_refresh"
	GrantClientCredentials GrantKind = "client_credentials"
	GrantAPIKey            GrantKind = "api_key"
)

// Capabilities says what a connector can do beyond reading.
type Capabilities struct {
	Webhooks, Note, Attachment bool
	CustomFieldSlots           int
	RequiresRevisionOnWrite    bool
}

// Credentials is loaded by the caller; Secret is never stored in Postgres.
type Credentials struct {
	Kind   GrantKind
	Secret []byte
}

// GrantMaterial is what Exchange and Refresh hand back for the caller to store.
type GrantMaterial struct {
	Kind      GrantKind
	Secret    []byte
	ExpiresAt *time.Time
}

// AuthorizeRequest carries the state the vendor echoes back to the callback.
type AuthorizeRequest struct{ State string }

// AuthorizeResult is built locally, with no network call.
type AuthorizeResult struct{ ConsentURL string }

type ExchangeRequest struct{ Code string }

// ExchangeResult is what the callback stores: the vendor's company key and the grant, whose Secret
// is connector-owned opaque bytes.
type ExchangeResult struct {
	ExternalKey string
	Grant       GrantMaterial
}

// DocRef identifies one revision of one vendor document.
type DocRef struct {
	SourceDocID, SourceRevision string
	Scope                       string // vendor scope: realmId, companyId, database
	Deleted                     bool   // deleted or voided in the vendor
}

// Changes is one page of ListChanged. Cursor is opaque to the caller.
type Changes struct {
	Refs   []DocRef
	Cursor []byte
	More   bool
}

type Document struct {
	Ref         DocRef
	ContentType string
	Body        []byte
}

type Party struct {
	ID, ContentType string
	Body            []byte
}

type Attachment struct {
	Name, ContentType string
	Body              []byte
}

type WriteBack struct {
	IRN, Note  string
	Fields     []string
	Attachment *Attachment
}

// Hint names a document a verified webhook says changed.
type Hint struct{ Scope, SourceDocID string }

// RateLimitError is a vendor throttle; callers find it with errors.As.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("erp: rate limited, retry after %s", e.RetryAfter)
}

var (
	ErrUnsupported  = errors.New("erp: not supported by this connector")
	ErrUnknownScope = errors.New("erp: unknown vendor scope")
	ErrNotFound     = errors.New("erp: not found in the vendor")
	ErrBadCursor    = errors.New("erp: cursor not recognised")
	// ErrGrantRevoked is permanent: the vendor ended the grant, so a refresh never retries it.
	ErrGrantRevoked = errors.New("erp: grant revoked by the vendor")
	// ErrRevisionConflict: a write named a revision the vendor no longer holds.
	ErrRevisionConflict = errors.New("erp: the document changed in the vendor")
)

// Connector is one vendor. WriteBack returns the new revision for vendors that
// RequiresRevisionOnWrite.
type Connector interface {
	System() string
	Capabilities() Capabilities
	Authorize(ctx context.Context, req AuthorizeRequest) (AuthorizeResult, error)
	Exchange(ctx context.Context, req ExchangeRequest) (ExchangeResult, error)
	Refresh(ctx context.Context, c Credentials) (GrantMaterial, error)
	Validate(ctx context.Context, c Credentials, scope string) error
	ListChanged(ctx context.Context, c Credentials, scope string, cursor []byte) (Changes, error)
	Fetch(ctx context.Context, c Credentials, ref DocRef) (Document, error)
	FetchParty(ctx context.Context, c Credentials, scope, partyID string) (Party, error)
	WriteBack(ctx context.Context, c Credentials, ref DocRef, wb WriteBack) (DocRef, error)
	VerifyWebhook(ctx context.Context, h http.Header, body []byte) ([]Hint, error)
	EnsureSubscriptions(ctx context.Context, c Credentials, scope string) error
}

// Registry maps System() to its connector.
type Registry map[string]Connector

// NewRegistry refuses a connector with an empty or duplicate System().
func NewRegistry(cs ...Connector) (Registry, error) {
	r := make(Registry, len(cs))
	for _, c := range cs {
		s := c.System()
		if s == "" {
			return nil, errors.New("erp: connector with an empty system")
		}
		if _, dup := r[s]; dup {
			return nil, fmt.Errorf("erp: duplicate connector for system %q", s)
		}
		r[s] = c
	}
	return r, nil
}

// Systems lists the registered systems, sorted, never nil.
func (r Registry) Systems() []string {
	out := make([]string, 0, len(r))
	for s := range r {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
