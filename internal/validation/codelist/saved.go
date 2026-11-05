package codelist

import (
	"embed"
	"encoding/json"
	"io/fs"
)

// saved holds the raw NRS body of each list, captured 2026-11-05T08:25Z from {DefaultBaseURL}/{list}.
//
//go:embed saved/*.json
var saved embed.FS

// savedCopy returns the saved copy of l, grouped like fetch. ok is false when l has none.
func savedCopy(fsys fs.FS, l List) (grouped map[string][]json.RawMessage, entries int, ok bool, err error) {
	body, rerr := fs.ReadFile(fsys, "saved/"+l.Name+".json")
	if rerr != nil {
		return nil, 0, false, nil
	}
	grouped, entries, err = decode(l, body)
	return grouped, entries, true, err
}
