package archive

// chunk splits ids into ordered batches of at most size (D-20's = ANY shape). Empty
// input yields nil, not an empty slice.
func chunk(ids []string, size int) [][]string {
	if len(ids) == 0 {
		return nil
	}
	out := make([][]string, 0, (len(ids)+size-1)/size)
	for i := 0; i < len(ids); i += size {
		end := i + size
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[i:end])
	}
	return out
}
