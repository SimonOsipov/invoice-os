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

// csvRecords writes records through encoding/csv and counts the rows after the header.
type csvRecords struct {
	name        string
	cw          *csv.Writer
	rows        int
	wroteHeader bool
}

// csvEntry buffers one CSV's records and hashes them as they're written, so the
// digest is ready the moment finalizeCSV materializes the ZIP entry.
type csvEntry struct {
	csvRecords
	buf  bytes.Buffer
	hash hash.Hash
}

func (bw *bundleWriter) newCSVEntry(name string) *csvEntry {
	e := &csvEntry{csvRecords: csvRecords{name: name}, hash: sha256.New()}
	e.cw = csv.NewWriter(io.MultiWriter(&e.buf, e.hash))
	return e
}

// Write satisfies csvWriter. The first call is the header record and is not
// counted in rows.
func (e *csvRecords) Write(record []string) error {
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

// WriteBody satisfies bodyWriter: it copies body verbatim into its own ZIP
// entry, never through a CSV cell.
func (bw *bundleWriter) WriteBody(name string, body []byte) error {
	return bw.WriteBodyFrom(name, bytes.NewReader(body))
}

// byteCounter counts the bytes written through it.
type byteCounter struct{ n int64 }

func (c *byteCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// WriteBodyFrom streams r verbatim into its own ZIP entry. A copy error returns
// before any manifest entry is appended.
func (bw *bundleWriter) WriteBodyFrom(name string, r io.Reader) error {
	w, err := bw.zw.Create(name)
	if err != nil {
		return fmt.Errorf("archive: create %s entry: %w", name, err)
	}
	h, n := sha256.New(), &byteCounter{}
	if _, err := io.Copy(io.MultiWriter(w, h, n), r); err != nil {
		return fmt.Errorf("archive: write %s entry: %w", name, err)
	}
	if err := bw.zw.Flush(); err != nil {
		return fmt.Errorf("archive: flush %s entry: %w", name, err)
	}
	bw.entries = append(bw.entries, manifestEntry{
		Name:   name,
		Bytes:  n.n,
		SHA256: hex.EncodeToString(h.Sum(nil)),
	})
	return nil
}

// streamCSV opens the entry first and lets fill write records straight into it.
// Nothing else may create an entry while fill runs: archive/zip keeps one open.
func (bw *bundleWriter) streamCSV(name string, fill func(csvWriter) error) error {
	w, err := bw.zw.Create(name)
	if err != nil {
		return fmt.Errorf("archive: create %s entry: %w", name, err)
	}
	h, n := sha256.New(), &byteCounter{}
	e := &csvRecords{name: name, cw: csv.NewWriter(io.MultiWriter(w, h, n))}
	if err := fill(e); err != nil {
		return err
	}
	e.cw.Flush()
	if err := e.cw.Error(); err != nil {
		return fmt.Errorf("archive: flush %s: %w", name, err)
	}
	if err := bw.zw.Flush(); err != nil {
		return fmt.Errorf("archive: flush %s entry: %w", name, err)
	}
	rows := e.rows
	bw.entries = append(bw.entries, manifestEntry{
		Name:   name,
		Bytes:  n.n,
		SHA256: hex.EncodeToString(h.Sum(nil)),
		Rows:   &rows,
	})
	return nil
}

// Close delegates to the underlying zip.Writer, which alone writes the central
// directory. Callers must not call this after a mid-stream error --
// leaving the writer unclosed is what makes the abandoned bytes unreadable.
func (bw *bundleWriter) Close() error {
	return bw.zw.Close()
}
