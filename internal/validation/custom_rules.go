package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/validation/codelist"
)

const (
	customKeyPrefix = "custom:"
	// customFaultMessage is the message of a custom rule that could not be evaluated on an item (D5).
	customFaultMessage = "This custom rule could not be evaluated on this invoice."
)

// customRuleWire is one custom rule as the caller sends it. ID is the
// revision uuid the result echoes in custom_rule_ids.
type customRuleWire struct {
	ID       string          `json:"id"`
	Key      string          `json:"key"`
	Type     RuleType        `json:"type"`
	Target   string          `json:"target"`
	Params   json.RawMessage `json:"params"`
	Severity Severity        `json:"severity"`
	When     *string         `json:"when"`
	Message  string          `json:"message"`
}

// customSets holds every custom rule of a batch once, flattened; byName maps a set key to indexes into rules.
type customSets struct {
	rules  []Rule
	revIDs []string
	byName map[string][]int
}

// newCustomSets validates the request's sets and the sets each item names. The
// error text is the 400 body.
func newCustomSets(eng *Engine, sets map[string][]customRuleWire, items []batchItem) (*customSets, error) {
	names := make([]string, 0, len(sets))
	for name := range sets {
		names = append(names, name)
	}
	sort.Strings(names)

	cs := &customSets{byName: make(map[string][]int, len(sets))}
	seenID := map[string]bool{}
	seenKey := map[string]bool{}
	for _, name := range names {
		wires := sets[name]
		if len(wires) == 0 {
			return nil, fmt.Errorf("custom_rule_sets[%q] must not be empty", name)
		}
		for _, w := range wires {
			if err := checkCustomRule(eng, w); err != nil {
				return nil, fmt.Errorf("custom_rule_sets[%q]: %w", name, err)
			}
			if seenID[w.ID] {
				return nil, fmt.Errorf("custom_rule_sets: rule id %q appears more than once", w.ID)
			}
			seenID[w.ID] = true
			if seenKey[w.Key] {
				return nil, fmt.Errorf("custom_rule_sets[%q]: duplicate key %q", name, w.Key)
			}
			seenKey[w.Key] = true

			cs.byName[name] = append(cs.byName[name], len(cs.rules))
			cs.rules = append(cs.rules, Rule{
				Key: w.Key, Type: w.Type, Target: w.Target, Params: w.Params,
				Severity: w.Severity, When: w.When, Message: w.Message,
				Scope: "document", Enabled: true,
			})
			cs.revIDs = append(cs.revIDs, w.ID)
		}
	}
	for i, it := range items {
		seen := map[string]bool{}
		for _, name := range it.CustomRuleSets {
			if _, ok := sets[name]; !ok {
				return nil, fmt.Errorf("invoices[%d] names unknown custom rule set %q", i, name)
			}
			if seen[name] {
				return nil, fmt.Errorf("invoices[%d] names custom rule set %q twice", i, name)
			}
			seen[name] = true
		}
	}
	return cs, nil
}

func checkCustomRule(eng *Engine, w customRuleWire) error {
	switch {
	case w.ID == "":
		return fmt.Errorf("rule id must not be empty")
	case !strings.HasPrefix(w.Key, customKeyPrefix) || len(w.Key) == len(customKeyPrefix):
		return fmt.Errorf("rule %q key must start with %q", w.ID, customKeyPrefix)
	}
	if _, ok := eng.registry[w.Type]; !ok {
		return fmt.Errorf("rule %q has an unknown type", w.ID)
	}
	if !validCustomSeverity(w.Severity) {
		return fmt.Errorf("rule %q severity must be error or warning", w.ID)
	}
	if w.Message == "" {
		return fmt.Errorf("rule %q message must not be empty", w.ID)
	}
	return nil
}

// evaluate judges one item by the named sets, one single-rule RuleSet per rule so
// a custom rule cannot touch a golden result (D4). A fault becomes a violation (D5).
// ids lists the revision ids evaluated, [] when the item names no set.
func (cs *customSets) evaluate(ctx context.Context, eng *Engine, p Payload, rs RuleSet, names []string, log *slog.Logger) (vs []Violation, ids []string) {
	ids = []string{}
	for _, name := range names {
		for _, i := range cs.byName[name] {
			r := cs.rules[i]
			ids = append(ids, cs.revIDs[i])
			res, err := eng.Evaluate(p, RuleSet{ID: rs.ID, Version: rs.Version, Rules: []Rule{r}})
			if err != nil {
				log.WarnContext(ctx, "validation: batch validate: custom rule fault",
					slog.String("rule_key", r.Key), slog.String("revision_id", cs.revIDs[i]), slog.Any("err", err))
				vs = append(vs, Violation{RuleKey: r.Key, Severity: r.Severity, Message: customFaultMessage, Path: r.Target})
				continue
			}
			vs = append(vs, res.Violations...)
		}
	}
	return vs, ids
}

// validCustomSeverity: a custom rule blocks (error) or warns (warning); info is refused (D3).
func validCustomSeverity(s Severity) bool { return s == "error" || s == "warning" }

// customRuleCheck is the POST /v1/custom-rules/check body.
type customRuleCheck struct {
	Type     string          `json:"type"`
	Target   string          `json:"target"`
	Params   json.RawMessage `json:"params"`
	Severity string          `json:"severity"`
	When     *string         `json:"when"`
	Message  string          `json:"message"`
}

// checkCustomRuleBody is the save-time check (D9): the staff desk's static checks, then
// the publish probe on an empty invoice and on the sample. The error text is the 422 reason.
func checkCustomRuleBody(eng *Engine, in customRuleCheck) error {
	if !validCustomSeverity(Severity(in.Severity)) {
		return errors.New("severity must be error or warning")
	}
	enabled := true
	v, err := validateDraftRule(eng, "custom", draftRuleRequest{
		Type: in.Type, Target: in.Target, Params: in.Params, Severity: in.Severity,
		When: in.When, Message: in.Message, Enabled: &enabled,
	})
	if err != nil {
		return err
	}
	r := Rule{Key: "custom", Type: RuleType(v.typ), Target: v.target, Params: json.RawMessage(v.params),
		Severity: Severity(v.severity), When: v.when, Message: v.message, Scope: "document", Enabled: true}
	if r.Type == TypeEnum {
		var p struct {
			List *string `json:"list"`
		}
		_ = json.Unmarshal(r.Params, &p)
		if p.List != nil && *p.List != "" {
			if !slices.ContainsFunc(codelist.Lists, func(l codelist.List) bool { return l.Name == *p.List }) {
				return errors.New("params.list names no NRS list")
			}
			// non-empty stand-in: enumEval faults on an empty set
			r.Codes = CodeSet{"": {}}
		}
	}
	var sample Payload
	if err := json.Unmarshal(sampleInvoiceJSON, &sample); err != nil {
		return fmt.Errorf("embedded sample invoice: %w", err)
	}
	for _, p := range []Payload{{"invoice": map[string]any{}}, sample} {
		if _, err := eng.Evaluate(p, RuleSet{Rules: []Rule{r}}); err != nil {
			return fmt.Errorf("the rule does not evaluate: %s", rootCause(err))
		}
	}
	return nil
}

// rootCause is the innermost error of the %w chain, minus the engine's "validation: " prefix.
func rootCause(err error) string {
	for u := errors.Unwrap(err); u != nil; u = errors.Unwrap(err) {
		err = u
	}
	return strings.TrimPrefix(err.Error(), "validation: ")
}

// CheckCustomRuleHandler returns POST /v1/custom-rules/check: 204 valid, 422 with a reason.
// S2S only, no DB, no tenant.
func CheckCustomRuleHandler(eng *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxDraftRuleBodyBytes)
		var in customRuleCheck
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds the size limit")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := checkCustomRuleBody(eng, in); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
