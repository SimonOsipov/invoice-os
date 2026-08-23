package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// bodyWriter is satisfied later by the streaming zip assembler (a later subtask) --
// selectExchange only defines the seam here. WriteBody creates one ZIP entry named
// name and writes body verbatim (D-6: bodies are files, never CSV cells).
type bodyWriter interface {
	WriteBody(name string, body []byte) error
}

// exchangeCSVHeader: bodies are file-name cells (request_body_file/
// response_body_file), never body bytes (D-6). poll_ref never appears here (D-10).
var exchangeCSVHeader = []string{
	"invoice_id", "invoice_number", "submission_job_id", "exchange_id", "operation",
	"outcome", "attempt", "http_status", "latency_ms", "truncated", "encoding_coerced",
	"request_headers", "response_headers", "request_body_file", "response_body_file",
	"adapter", "adapter_version", "occurred_at",
}

// exchangeScope: the FROM/WHERE selectExchangeSQL and countExchangeSQL share (D-47).
const exchangeScope = `
  FROM app_exchange
 WHERE invoice_id = ANY($1::uuid[])`

// selectExchangeSQL: invoice_number comes from invoiceNumbers, never a JOIN against
// invoices (see TestExchangeSQL_ContainsNoJoinAgainstInvoices).
const selectExchangeSQL = `
SELECT id, submission_job_id, invoice_id, operation, outcome, attempt, http_status,
       latency_ms, truncated, encoding_coerced, request_headers, response_headers,
       request_body, response_body, adapter, adapter_version, occurred_at` +
	exchangeScope + `
 ORDER BY invoice_id, occurred_at, id`

// countExchangeSQL returns exchange_attempts and body_files from ONE statement, so
// the two numbers can never come from different rows (D-47, subtask-09): the second
// column is request_body's non-NULL count plus response_body's, matching
// selectExchange's own "write bodies/<id>.request|.response when non-nil" rule
// exactly, so IS NOT NULL cannot drift from Go's != nil (including an empty-string
// body, which is representable and must still count).
const countExchangeSQL = `SELECT count(*),
       count(*) FILTER (WHERE request_body IS NOT NULL)
     + count(*) FILTER (WHERE response_body IS NOT NULL)` + exchangeScope

// selectExchange writes exchange.csv, re-scrubbing both header maps through
// submission.ScrubHeaders on the way OUT regardless of what write time stored (D-7),
// so AC 8 holds even for a row planted directly against the table. Bodies stream out
// one row at a time through bw, never accumulated in memory (D-20).
func selectExchange(ctx context.Context, tx pgx.Tx, ids []string, w csvWriter, bw bodyWriter) error {
	if err := w.Write(exchangeCSVHeader); err != nil {
		return fmt.Errorf("archive: write exchange.csv header: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	numbers, err := invoiceNumbers(ctx, tx, ids)
	if err != nil {
		return err
	}

	for _, batch := range chunk(ids, 500) {
		rows, err := tx.Query(ctx, selectExchangeSQL, batch)
		if err != nil {
			return fmt.Errorf("archive: select app_exchange: %w", err)
		}
		for rows.Next() {
			var id, submissionJobID, invoiceID, operation, outcome string
			var attempt int
			var httpStatus, latencyMs *int
			var truncated, encodingCoerced bool
			var requestHeadersRaw, responseHeadersRaw string
			var requestBody, responseBody *string
			var adapter, adapterVersion string
			var occurredAt time.Time
			if err := rows.Scan(&id, &submissionJobID, &invoiceID, &operation, &outcome, &attempt,
				&httpStatus, &latencyMs, &truncated, &encodingCoerced,
				&requestHeadersRaw, &responseHeadersRaw, &requestBody, &responseBody,
				&adapter, &adapterVersion, &occurredAt); err != nil {
				rows.Close()
				return fmt.Errorf("archive: scan app_exchange row: %w", err)
			}

			reqHeaders, err := rescrubHeaders(requestHeadersRaw)
			if err != nil {
				rows.Close()
				return fmt.Errorf("archive: rescrub request_headers for exchange %s: %w", id, err)
			}
			respHeaders, err := rescrubHeaders(responseHeadersRaw)
			if err != nil {
				rows.Close()
				return fmt.Errorf("archive: rescrub response_headers for exchange %s: %w", id, err)
			}

			requestBodyFile := ""
			if requestBody != nil {
				requestBodyFile = "bodies/" + id + ".request"
				if err := bw.WriteBody(requestBodyFile, []byte(*requestBody)); err != nil {
					rows.Close()
					return fmt.Errorf("archive: write request body for exchange %s: %w", id, err)
				}
			}
			responseBodyFile := ""
			if responseBody != nil {
				responseBodyFile = "bodies/" + id + ".response"
				if err := bw.WriteBody(responseBodyFile, []byte(*responseBody)); err != nil {
					rows.Close()
					return fmt.Errorf("archive: write response body for exchange %s: %w", id, err)
				}
			}

			record := []string{
				invoiceID,
				numbers[invoiceID],
				submissionJobID,
				id,
				operation,
				outcome,
				strconv.Itoa(attempt),
				emptyIntIfNil(httpStatus),
				emptyIntIfNil(latencyMs),
				strconv.FormatBool(truncated),
				strconv.FormatBool(encodingCoerced),
				reqHeaders,
				respHeaders,
				requestBodyFile,
				responseBodyFile,
				adapter,
				adapterVersion,
				occurredAt.UTC().Format(time.RFC3339Nano),
			}
			if err := w.Write(record); err != nil {
				rows.Close()
				return fmt.Errorf("archive: write exchange.csv row: %w", err)
			}
		}
		iterErr := rows.Err()
		rows.Close()
		if iterErr != nil {
			return fmt.Errorf("archive: iterate app_exchange: %w", iterErr)
		}
	}
	return nil
}

// rescrubHeaders unmarshals a jsonb header-map cell, re-applies the REAL
// submission.ScrubHeaders (never a copied allowlist), and re-marshals -- already
// compact from Go's own json.Marshal, unlike invoices.go's compactJSON which fixes up
// Postgres's own jsonb print spacing.
func rescrubHeaders(raw string) (string, error) {
	var h http.Header
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return "", fmt.Errorf("unmarshal headers %q: %w", raw, err)
	}
	scrubbed, err := json.Marshal(submission.ScrubHeaders(h))
	if err != nil {
		return "", fmt.Errorf("marshal scrubbed headers: %w", err)
	}
	return string(scrubbed), nil
}

// emptyIntIfNil mirrors emptyIfNil for http_status/latency_ms.
func emptyIntIfNil(n *int) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(*n)
}
