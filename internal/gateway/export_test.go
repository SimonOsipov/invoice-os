package gateway

import "github.com/SimonOsipov/invoice-os/internal/platform"

// BuildSHAForTest exposes the compiled-in sha to this package's tests without
// re-importing platform in every file.
func BuildSHAForTest() string { return platform.BuildSHA }
