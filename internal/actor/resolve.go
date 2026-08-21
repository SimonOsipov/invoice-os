package actor

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// systemActor is the stored subject for an action no person took.
const systemActor = "system"

// uuidShape is the audit trigger's own gate, byte for byte
// (migrations/20260820150810_audit_log_entity_id_and_read_indexes.sql:70-75), so
// the Go copy accepts exactly what uuid_in accepts. Stricter would be a silent
// wrong answer, not an error: a subject the trigger indexed would render raw.
// Fenced by TestActorResolve_UUIDGateMatchesUUIDIn.
var uuidShape = regexp.MustCompile(`^[0-9a-f]{4}(-?[0-9a-f]{4}){7}$`)

// normalizeUUID reports whether subject is a uuid to Postgres, and returns the
// lower-cased, hyphen-stripped form the trigger casts.
func normalizeUUID(subject string) (string, bool) {
	s := strings.ToLower(subject)
	// The length guard mirrors LIKE '{%}', which cannot match a lone brace.
	if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
		s = s[1 : len(s)-1]
	}
	if !uuidShape.MatchString(s) {
		return "", false
	}
	return strings.ReplaceAll(s, "-", ""), true
}

// Resolve maps every stored audit_log actor string to a display Label in at most
// one round trip: "system" and anything failing the UUID gate are classified in
// Go and never bound; the rest are de-duplicated on their normalised uuid and
// looked up in a single `= ANY($1::uuid[])` over memberships.
//
// Scope is the caller's and nothing else's: Resolve opens no transaction, sets no
// GUC and writes no tenant_id predicate. The memberships tenant_isolation policy
// on tx's connection is the only filter, so a tx with no app.current_tenant
// resolves nothing and a superuser tx would resolve across tenants.
func Resolve(ctx context.Context, tx pgx.Tx, subjects []string) (map[string]Label, error) {
	out := make(map[string]Label, len(subjects))
	// De-duplicated on the normalised uuid, keyed on the raw subject: two
	// spellings of one id bind once and key twice.
	subjectsOf := make(map[string][]string)
	var bind []string

	for _, subject := range subjects {
		if _, done := out[subject]; done {
			continue
		}
		if subject == systemActor {
			out[subject] = Label{Text: "System", Kind: KindSystem}
			continue
		}
		// A uuid nothing can name stays raw; a row below overwrites it.
		out[subject] = Label{Text: subject, Kind: KindRaw}
		norm, ok := normalizeUUID(subject)
		if !ok {
			continue
		}
		if _, seen := subjectsOf[norm]; !seen {
			bind = append(bind, norm)
		}
		subjectsOf[norm] = append(subjectsOf[norm], subject)
	}

	if len(bind) == 0 {
		return out, nil
	}

	rows, err := tx.Query(ctx,
		`SELECT user_id, display_name, email
		   FROM memberships
		  WHERE user_id = ANY($1::uuid[])`, bind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID string
		var displayName, email *string
		if err := rows.Scan(&userID, &displayName, &email); err != nil {
			return nil, err
		}
		for _, subject := range subjectsOf[strings.ReplaceAll(userID, "-", "")] {
			out[subject] = Name(displayName, email, subject)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
