package platform

import "regexp"

// PostureKind is a deployment posture.
type PostureKind string

const (
	PostureLocal   PostureKind = "local"
	PosturePreview PostureKind = "preview"
	PostureHosted  PostureKind = "hosted"
)

// Same shape as internal/platform/db's prEnvironmentPattern, duplicated because that
// one is unexported and its package must not be touched here.
var prNamePattern = regexp.MustCompile(`^(?:.+-)?pr-[0-9]+$`)

// Posture derives deployment posture from RAILWAY_ENVIRONMENT_NAME. Railway injects it
// per environment instead of forking it as a value, so unlike ENVIRONMENT it still tells
// a PR fork from its source after an environment is renamed. Empty (local, CI, go test)
// is the only input reaching the permissive value; any unrecognized name is Hosted.
func Posture(railwayEnvironmentName string) PostureKind {
	switch {
	case railwayEnvironmentName == "":
		return PostureLocal
	case prNamePattern.MatchString(railwayEnvironmentName):
		return PosturePreview
	default:
		return PostureHosted
	}
}
