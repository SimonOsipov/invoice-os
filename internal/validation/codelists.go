package validation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/validation/codelist"
)

// ErrCodeListMissing means an enabled list rule names a list with no synced
// rows. It wraps ErrNoActiveRuleSet, so the batch handler answers 503.
var ErrCodeListMissing = fmt.Errorf("%w: code list missing", ErrNoActiveRuleSet)

// attachCodeLists fills Codes on each enabled document-scope list rule, with
// one query for all named lists. Rules the engine skips, and rules whose
// params do not decode, are left for the evaluator to report.
func attachCodeLists(ctx context.Context, tx pgx.Tx, rules []Rule) error {
	byName, names := listRuleNames(rules)
	if len(names) == 0 {
		return nil
	}
	lists, err := codelist.LoadTx(ctx, tx, names)
	if err != nil {
		return err
	}
	for _, name := range names {
		codes, ok := lists[name]
		if !ok {
			return fmt.Errorf("%w: %s", ErrCodeListMissing, name)
		}
		for _, i := range byName[name] {
			rules[i].Codes = codes
		}
	}
	return nil
}

// attachCustomCodeLists is attachCodeLists for custom rules: a list with no
// rows is returned by name, its rules keep nil Codes and fault at evaluation (D5).
func attachCustomCodeLists(ctx context.Context, tx pgx.Tx, rules []Rule) (missing []string, err error) {
	byName, names := listRuleNames(rules)
	if len(names) == 0 {
		return nil, nil
	}
	lists, err := codelist.LoadTx(ctx, tx, names)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		codes, ok := lists[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		for _, i := range byName[name] {
			rules[i].Codes = codes
		}
	}
	return missing, nil
}

// listRuleNames indexes the enabled document-scope list rules by list name.
func listRuleNames(rules []Rule) (byName map[string][]int, names []string) {
	byName = map[string][]int{}
	for i, r := range rules {
		if r.Type != TypeEnum || !r.Enabled || r.Scope != "document" {
			continue
		}
		var params struct {
			List *string `json:"list"`
		}
		if json.Unmarshal(r.Params, &params) != nil || params.List == nil || *params.List == "" {
			continue
		}
		if _, seen := byName[*params.List]; !seen {
			names = append(names, *params.List)
		}
		byName[*params.List] = append(byName[*params.List], i)
	}
	return byName, names
}
