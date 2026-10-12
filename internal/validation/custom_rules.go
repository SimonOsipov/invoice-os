package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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
	if w.Severity != "error" && w.Severity != "warning" {
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
