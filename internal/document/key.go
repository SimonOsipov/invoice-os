package document

// StorageKey returns the object key for one stored document.
//
// Both segments are server-derived — tenantID from the verified JWT, contentHash
// from the bytes just received — so there is no path from caller-supplied text
// to a key. Per-tenant prefixing keeps byte-identical uploads from two tenants
// on two distinct objects.
func StorageKey(tenantID, contentHash string) string {
	return "tenants/" + tenantID + "/" + contentHash
}
