package codelist

import (
	"embed"
	"encoding/json"
	"io/fs"
)

// saved holds one NRS-envelope file per list; see each file's "source" key for hand-built ones.
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
