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
)

// Same shape as internal/platform/db's prEnvironmentPattern, duplicated because that
// one is unexported and its package must not be touched here.
var prNamePattern = regexp.MustCompile(`^(?:.+-)?pr-[0-9]+$`)

// DeployEnv names the deployment: "production", "pr-<N>", or empty when local.
// It reads DEPLOY_ENV, else Railway's injected name.
// ceiling: Railway fallback, delete with Railway in GKE-12.
func DeployEnv() string {
	if v := os.Getenv("DEPLOY_ENV"); v != "" {
		return v
	}
	return os.Getenv("RAILWAY_ENVIRONMENT_NAME")
}

// Posture derives deployment posture from DeployEnv. Empty (local, CI, go test) is the
// only input reaching the permissive value; any unrecognized name is Hosted.
func Posture(deployEnv string) PostureKind {
	switch {
	case deployEnv == "":
		return PostureLocal
	case prNamePattern.MatchString(deployEnv):
		return PosturePreview
	default:
		return PostureHosted
	}
}
