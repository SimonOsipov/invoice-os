package platform

import (
	"os"
	"regexp"
)

// PostureKind is a deployment posture.
type PostureKind string

const (
	PostureLocal   PostureKind = "local"
	PosturePreview PostureKind = "preview"
	PostureHosted  PostureKind = "hosted"
	PostureNightly PostureKind = "nightly"
)

// Ephemeral reports a preview or the nightly.
func (p PostureKind) Ephemeral() bool { return p == PosturePreview || p == PostureNightly }

// Same shape as internal/platform/db's prEnvironmentPattern, duplicated because that
// one is unexported and its package must not be touched here.
var prNamePattern = regexp.MustCompile(`^(?:.+-)?pr-[0-9]+$`)

// DeployEnv names the deployment from DEPLOY_ENV: "production", "nightly", "pr-<N>", or empty when local.
func DeployEnv() string { return DeployEnvFrom(os.Getenv) }

// DeployEnvFrom is DeployEnv over an injected environment.
func DeployEnvFrom(getenv func(string) string) string {
	return getenv("DEPLOY_ENV")
}

// Posture derives deployment posture from DeployEnv. Empty (local, CI, go test) is the
// only input reaching the permissive value; "nightly" matches exactly; any other unrecognized name is Hosted.
func Posture(deployEnv string) PostureKind {
	switch {
	case deployEnv == "":
		return PostureLocal
	case deployEnv == "nightly":
		return PostureNightly
	case prNamePattern.MatchString(deployEnv):
		return PosturePreview
	default:
		return PostureHosted
	}
}
