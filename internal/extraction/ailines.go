// ailines.go: the AI's separate line-item call -- prompt, schema, answer type and askAILines.
// aireading.go's askAI reads header fields; this reads line items (Core AC 5).
package extraction

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

// aiLinesSystem is tools/aimodeltest/run.py's LINE_SYSTEM, byte for byte
// (TestAILinesSystem_MatchesTheMeasuredHarness): the prompt Part A measured at 611/612 value
// cells.
const aiLinesSystem = `You extract the line items of one invoice for a Nigerian e-invoicing system.

Return the invoice's printed line-item rows under the key line_items, in printed order, top to bottom. Return an empty list when the document prints no line-item rows at all.

One object per printed row. Never merge two printed rows into one, never split one printed row into two, never invent a row that is not printed, and never reorder the rows.

A totals band is never a line item. A subtotal, tax, total, amount-due or balance line below or beside the rows is not a row, even when it repeats a row's figures.

Each object has exactly these keys. Each value is a string, or null when the row does not print that cell. Never guess, compute or infer a value that is not printed.
- description: what the row is for, exactly as printed. When a description is printed over more than one line, join the lines with one space.
- quantity: the number of units, exactly as printed.
- unit_price: the price of one unit, as digits with a decimal point and no currency symbol or thousands separators (1250.00).
- line_total: the row's own amount, same number format.
- line_tax: the tax amount charged on this row, same number format. A tax rate (7.5%) is not a tax amount; when the row prints only a rate, line_tax is null.`

// aiLinesSchema is the wrapped array schema run.py's LINES_SCHEMA sends: one line_items
// property, an array of aiSchemaFor(LineRoles) objects.
var aiLinesSchema = aiSchemaForLines()

func aiSchemaForLines() json.RawMessage {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"line_items"},
		"properties": map[string]any{
			"line_items": map[string]any{
				"type":  "array",
				"items": aiSchemaFor(LineRoles), // json.RawMessage inlines, no double-encoding
			},
		},
	}
	b, _ := json.Marshal(schema) // fixed, marshalable shape: cannot fail
	return b
}

// AILine is one line_items row the AI answered: the five role cells, nil when the row prints
// no value there -- absent and blank are one state, matching askAI's own blank filter.
type AILine struct {
	Description *string
	Quantity    *string
	UnitPrice   *string
	LineTotal   *string
	LineTax     *string
}

// askAILines asks once for the document's line items. The bool reports a failed call
// (aiFailed); the answer is nil when there is nothing to add -- an off reader, no line_items
// key, JSON null, an empty array, or a malformed row (never a silently dropped row).
func askAILines(ctx context.Context, r AIReader, pages []TokenPage) ([]AILine, bool) {
	ans, failed := aiCallHead(ctx, r, ai.Request{
		Purpose:    ai.PurposeLineItems,
		System:     aiLinesSystem,
		Text:       aiPromptText(pages),
		FakeScope:  "LINES",
		SchemaName: "invoice_line_items",
		Schema:     aiLinesSchema,
	})
	if failed || ans == nil {
		return nil, failed
	}

	raw, ok := ans["line_items"]
	if !ok || raw == nil {
		return nil, false
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, true
	}
	if len(items) == 0 {
		return nil, false
	}

	lines := make([]AILine, 0, len(items))
	for _, elem := range items {
		line, ok := aiLineFrom(elem)
		if !ok {
			return nil, true
		}
		lines = append(lines, line)
	}
	return lines, false
}

// aiLineFrom decodes one line_items element. ok is false when elem is not a JSON object, or a
// role cell is neither a string nor JSON null.
func aiLineFrom(elem any) (AILine, bool) {
	obj, ok := elem.(map[string]any)
	if !ok {
		return AILine{}, false
	}
	cells := make(map[string]*string, len(LineRoles))
	for _, role := range LineRoles {
		v, present := obj[role]
		if !present || v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return AILine{}, false
		}
		if strings.TrimSpace(s) == "" {
			continue
		}
		cells[role] = &s
	}
	return AILine{
		Description: cells[LineRoleDescription],
		Quantity:    cells[LineRoleQuantity],
		UnitPrice:   cells[LineRoleUnitPrice],
		LineTotal:   cells[LineRoleLineTotal],
		LineTax:     cells[LineRoleLineTax],
	}, true
}
