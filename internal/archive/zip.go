package archive

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

// bundleWriter assembles the evidence ZIP over a plain io.Writer (never an
// io.Seeker). archive/zip keeps exactly one entry open at a time (Writer.prepare
// force-closes the previous one on every Create), so a CSV's records are
// buffered here and only become a real ZIP entry once its stage finishes
// (finalizeCSV); WriteBody streams straight to a live entry (D-31).
type bundleWriter struct {
	zw      *zip.Writer
	entries []manifestEntry
}

// newBundleWriter never leaves entries nil -- see TestManifest_EntriesNeverMarshalsNull.
func newBundleWriter(w io.Writer) *bundleWriter {
	return &bundleWriter{zw: zip.NewWriter(w), entries: []manifestEntry{}}
}

// csvEntry buffers one CSV's records and hashes them as they're written, so the
// digest is ready the moment finalizeCSV materializes the ZIP entry.
type csvEntry struct {
	name        string
	buf         bytes.Buffer
	hash        hash.Hash
	cw          *csv.Writer
	rows        int
	wroteHeader bool
}

func (bw *bundleWriter) newCSVEntry(name string) *csvEntry {
	e := &csvEntry{name: name, hash: sha256.New()}
	e.cw = csv.NewWriter(io.MultiWriter(&e.buf, e.hash))
	return e
}

// Write satisfies csvWriter. The first call is the header record and is not
// counted in rows.
func (e *csvEntry) Write(record []string) error {
	if err := e.cw.Write(record); err != nil {
		return fmt.Errorf("archive: write %s record: %w", e.name, err)
	}
	if !e.wroteHeader {
		e.wroteHeader = true
		return nil
	}
	e.rows++
	return nil
}

// finalizeCSV flushes e's buffered bytes into a single real ZIP entry and
// records it in the manifest.
func (bw *bundleWriter) finalizeCSV(e *csvEntry) error {
	e.cw.Flush()
	if err := e.cw.Error(); err != nil {
		return fmt.Errorf("archive: flush %s: %w", e.name, err)
	}
	w, err := bw.zw.Create(e.name)
	if err != nil {
		return fmt.Errorf("archive: create %s entry: %w", e.name, err)
	}
	if _, err := w.Write(e.buf.Bytes()); err != nil {
		return fmt.Errorf("archive: write %s entry: %w", e.name, err)
	}
	// Push this entry's bytes past zip.Writer's internal 4096-byte bufio buffer now,
	// rather than waiting for Close() -- an abandoned writer must leave real bytes
	// behind (see TestBundleWriter_AbandonedWithoutCloseIsUnreadable).
	if err := bw.zw.Flush(); err != nil {
		return fmt.Errorf("archive: flush %s entry: %w", e.name, err)
	}
	rows := e.rows
	bw.entries = append(bw.entries, manifestEntry{
		Name:   e.name,
		Bytes:  int64(e.buf.Len()),
		SHA256: hex.EncodeToString(e.hash.Sum(nil)),
		Rows:   &rows,
	})
	return nil
}

// WriteBody satisfies bodyWriter: it creates its own ZIP entry and copies body
// verbatim, never through a CSV cell (D-6).
func (bw *bundleWriter) WriteBody(name string, body []byte) error {
	w, err := bw.zw.Create(name)
	if err != nil {
		return fmt.Errorf("archive: create %s entry: %w", name, err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("archive: write %s entry: %w", name, err)
	}
	if err := bw.zw.Flush(); err != nil {
		return fmt.Errorf("archive: flush %s entry: %w", name, err)
	}
	sum := sha256.Sum256(body)
	bw.entries = append(bw.entries, manifestEntry{
		Name:   name,
		Bytes:  int64(len(body)),
		SHA256: hex.EncodeToString(sum[:]),
	})
	return nil
}

// Close delegates to the underlying zip.Writer, which alone writes the central
// directory. Callers must not call this after a mid-stream error (D-15) --
// leaving the writer unclosed is what makes the abandoned bytes unreadable.
func (bw *bundleWriter) Close() error {
	return bw.zw.Close()
}
