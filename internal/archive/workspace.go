// workspace.go: the admin's export of a whole workspace.
package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ObjectOpener opens a stored document by its storage key.
type ObjectOpener func(ctx context.Context, key string) (io.ReadCloser, error)

// one export per instance: it pins a pool connection for up to an hour
const maxConcurrentExports = 1

var (
	ErrExportNotPermitted = errors.New("archive: only an admin can download the workspace export")
	ErrExportBusy         = errors.New("archive: another workspace export is running")
	ErrExportRunning      = errors.New("archive: an export of this workspace is already running")
)

// one snapshot; read-write because workspace.exported commits with the read
var workspaceExportTxOptions = pgx.TxOptions{IsoLevel: pgx.RepeatableRead}

var allTime = Request{From: time.Time{}, To: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)}

const documentPageSize = 500

var documentExtPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,10}$`)

// WorkspaceStore exports a whole workspace. The pool is the app-role pool.
type WorkspaceStore struct {
	pool        *pgxpool.Pool
	open        ObjectOpener
	slots       chan struct{}
	auditFailed func(ctx context.Context, id auth.Identity, files int, bytes int64) error
}

func NewWorkspaceStore(pool *pgxpool.Pool, open ObjectOpener) *WorkspaceStore {
	s := &WorkspaceStore{pool: pool, open: open, slots: make(chan struct{}, maxConcurrentExports)}
	s.auditFailed = s.recordExportFailed
	return s
}

// exportKey is the advisory-lock key of a workspace's export.
const exportKey = `hashtextextended('workspace-export:' || $1::text, 0)`

// sentCounter counts the bytes handed to w; wrote is set at the first Write call.
type sentCounter struct {
	w     io.Writer
	n     int64
	wrote bool
}

func (c *sentCounter) Write(p []byte) (int, error) {
	c.wrote = true
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// claimWorkspaceExport takes the workspace's export lock for the rest of the transaction.
// The try-lock is atomic, so of two simultaneous starts exactly one wins.
func claimWorkspaceExport(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var got bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(`+exportKey+`)`, tenantID).Scan(&got); err != nil {
		return fmt.Errorf("archive: claim export: %w", err)
	}
	if !got {
		return ErrExportRunning
	}
	return nil
}

// exportRunning reads the lock without taking it, so a preflight never blocks an export.
// pg_locks spans the cluster; PR databases share it, so filter to this database.
func exportRunning(ctx context.Context, tx pgx.Tx, tenantID string) (bool, error) {
	var running bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks, (SELECT `+exportKey+` AS k) x
		WHERE locktype = 'advisory' AND granted AND objsubid = 1
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		AND classid::bigint = (x.k >> 32) & 4294967295 AND objid::bigint = x.k & 4294967295)`,
		tenantID).Scan(&running)
	if err != nil {
		return false, fmt.Errorf("archive: read running exports: %w", err)
	}
	return running, nil
}

// PreflightWorkspace tells whether the caller's export can start. It takes no slot.
func (s *WorkspaceStore) PreflightWorkspace(ctx context.Context) error {
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return db.ErrNoTenant
	}
	return db.WithinRequestTenantTxOpts(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(ctx, `SELECT role FROM memberships WHERE user_id = $1`, id.Subject).Scan(&role); err != nil {
			return fmt.Errorf("archive: read role: %w", err)
		}
		if role != "admin" {
			return ErrExportNotPermitted
		}
		running, err := exportRunning(ctx, tx, id.TenantID)
		if err != nil {
			return err
		}
		if running {
			return ErrExportRunning
		}
		return nil
	})
}

type workspaceDocument struct {
	id, hash, key string
	filename      *string
	createdAt     time.Time
}

// AssembleWorkspace streams the whole workspace to w as one ZIP. It skips the seam, which refuses a
// read-write transaction of a locked workspace, and gates the active admin itself.
func (s *WorkspaceStore) AssembleWorkspace(ctx context.Context, w io.Writer, onStart func(filename string)) error {
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return db.ErrNoTenant
	}
	if _, err := uuid.Parse(id.Subject); err != nil {
		return db.ErrNoTenant
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return ErrExportBusy
	}

	var (
		bw   *bundleWriter
		sent = &sentCounter{w: w}
	)
	err := db.WithinTenantTxOpts(ctx, s.pool, id.TenantID, workspaceExportTxOptions, func(tx pgx.Tx) error {
		var status, role string
		if err := tx.QueryRow(ctx, `SELECT status, role FROM memberships WHERE user_id = $1`, id.Subject).Scan(&status, &role); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return db.ErrNotActiveMember
			}
			return fmt.Errorf("archive: read membership: %w", err)
		}
		if status != "active" {
			return db.ErrNotActiveMember
		}
		if role != "admin" {
			return ErrExportNotPermitted
		}
		if err := claimWorkspaceExport(ctx, tx, id.TenantID); err != nil {
			return err
		}

		var tenantName string
		if err := tx.QueryRow(ctx, `SELECT name FROM tenants`).Scan(&tenantName); err != nil {
			return fmt.Errorf("archive: read tenant: %w", err)
		}
		generatedBy, err := resolveGeneratedBy(ctx, tx, id.Subject)
		if err != nil {
			return err
		}
		companies, err := selectWorkspaceCompanies(ctx, tx)
		if err != nil {
			return err
		}

		now := time.Now()
		if onStart != nil {
			onStart("ASComply_workspace_" + entitySlug(tenantName, id.TenantID) + "_" + now.UTC().Format("20060102") + ".zip")
		}
		bw = newBundleWriter(sent)

		taken := map[string]bool{}
		// ceiling: one company's largest CSV buffers whole (~1 KB per invoice row at peak); stream the stages above ~150k invoices in one company
		for i := range companies {
			dir := companyDir(companies[i].Name, companies[i].ID, taken)
			taken[dir] = true
			companies[i].Dir = dir
			req := allTime
			req.EntityID = companies[i].ID
			if err := writeEntityStages(ctx, tx, bw, "companies/"+dir+"/", req); err != nil {
				return err
			}
		}
		if err := s.writeDocuments(ctx, tx, bw); err != nil {
			return err
		}
		if err := bw.streamCSV("audit_log.csv", func(cw csvWriter) error {
			_, err := audit.WriteExportCSV(ctx, tx, cw)
			return err
		}); err != nil {
			return err
		}

		files, bytes := len(bw.entries), int64(0)
		for _, e := range bw.entries {
			bytes += e.Bytes
		}
		if err := bw.writeWorkspaceManifest(id.TenantID, generatedBy, companies, now); err != nil {
			return err
		}
		return audit.Record(ctx, tx, id.Subject, "workspace.exported", map[string]any{"files": files, "bytes": bytes})
	})
	if err == nil {
		// Close alone writes the central directory; a failure here keeps the committed row.
		return bw.Close()
	}
	if sent.wrote {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if aerr := s.auditFailed(actx, id, len(bw.entries), sent.n); aerr != nil {
			slog.Default().ErrorContext(ctx, "archive: workspace export failure not audited", slog.Any("err", aerr))
		}
	}
	return err
}

func selectWorkspaceCompanies(ctx context.Context, tx pgx.Tx) ([]workspaceCompany, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, name, tin FROM business_entities ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("archive: select companies: %w", err)
	}
	defer rows.Close()
	out := []workspaceCompany{}
	for rows.Next() {
		var c workspaceCompany
		if err := rows.Scan(&c.ID, &c.Name, &c.TIN); err != nil {
			return nil, fmt.Errorf("archive: scan company: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("archive: select companies: %w", err)
	}
	return out, nil
}

// writeDocuments streams each documents row's object, in pages of 500.
func (s *WorkspaceStore) writeDocuments(ctx context.Context, tx pgx.Tx, bw *bundleWriter) error {
	var cur *workspaceDocument
	for {
		page, err := selectDocumentPage(ctx, tx, cur)
		if err != nil {
			return err
		}
		for _, d := range page {
			if err := s.writeDocument(ctx, bw, d); err != nil {
				return err
			}
		}
		if len(page) < documentPageSize {
			return nil
		}
		cur = &page[len(page)-1]
	}
}

func (s *WorkspaceStore) writeDocument(ctx context.Context, bw *bundleWriter, d workspaceDocument) error {
	body, err := s.open(ctx, d.key)
	if err != nil {
		return fmt.Errorf("archive: open document: %w", err)
	}
	defer body.Close()
	return bw.WriteBodyFrom("documents/"+d.hash+documentExt(d.filename), body)
}

func selectDocumentPage(ctx context.Context, tx pgx.Tx, cur *workspaceDocument) ([]workspaceDocument, error) {
	const cols = `SELECT id::text, content_hash, filename, storage_key, created_at FROM documents`
	var (
		rows pgx.Rows
		err  error
	)
	if cur == nil {
		rows, err = tx.Query(ctx, cols+` ORDER BY created_at, id LIMIT $1`, documentPageSize)
	} else {
		rows, err = tx.Query(ctx, cols+` WHERE (created_at, id) > ($1, $2::uuid) ORDER BY created_at, id LIMIT $3`,
			cur.createdAt, cur.id, documentPageSize)
	}
	if err != nil {
		return nil, fmt.Errorf("archive: select documents: %w", err)
	}
	defer rows.Close()
	var out []workspaceDocument
	for rows.Next() {
		var d workspaceDocument
		if err := rows.Scan(&d.id, &d.hash, &d.filename, &d.key, &d.createdAt); err != nil {
			return nil, fmt.Errorf("archive: scan document: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("archive: select documents: %w", err)
	}
	return out, nil
}

// recordExportFailed audits a failed export in its own short transaction; it works in a locked workspace.
func (s *WorkspaceStore) recordExportFailed(ctx context.Context, id auth.Identity, files int, bytes int64) error {
	return db.WithinTenantTxOpts(ctx, s.pool, id.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, id.Subject, "workspace.export_failed", map[string]any{"files": files, "bytes": bytes})
	})
}

// companyDir is the lower-cased slug of the name; a slug already taken gets "-<id>".
func companyDir(name, id string, taken map[string]bool) string {
	slug := strings.ToLower(entitySlug(name, id))
	if taken[slug] {
		return slug + "-" + id
	}
	return slug
}

// documentExt is the lower-cased extension after the last dot when it is 1 to 10 alphanumerics.
func documentExt(filename *string) string {
	if filename == nil {
		return ""
	}
	i := strings.LastIndex(*filename, ".")
	if i < 0 {
		return ""
	}
	ext := (*filename)[i+1:]
	if !documentExtPattern.MatchString(ext) {
		return ""
	}
	return "." + strings.ToLower(ext)
}
