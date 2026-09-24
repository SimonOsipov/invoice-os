package main

import (
	"fmt"
	"regexp"
	"strings"
)

var digestRE = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// ParsePin returns the image tag from the single digest-pinned, non-Docker-Hub
// FROM line in dockerfile.
func ParsePin(dockerfile string) (string, error) {
	var refs []string
	for _, line := range strings.Split(dockerfile, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		ref := ""
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "--") {
				ref = f
				break
			}
		}
		refs = append(refs, ref)
	}
	if len(refs) != 1 {
		return "", fmt.Errorf("want exactly one FROM line, found %d", len(refs))
	}
	ref := refs[0]
	if !digestRE.MatchString(ref) {
		return "", fmt.Errorf("FROM %q is not pinned by a sha256 digest", ref)
	}
	name, _, _ := strings.Cut(ref, "@")
	host, rest, ok := strings.Cut(name, "/")
	// Docker reads a first component without "." or ":" as a Docker Hub namespace.
	if !ok || !(strings.ContainsAny(host, ".:") || host == "localhost") {
		return "", fmt.Errorf("FROM %q names a Docker Hub image; the Railway builder cannot reach Docker Hub", ref)
	}
	if host == "docker.io" || strings.HasSuffix(host, ".docker.io") {
		return "", fmt.Errorf("FROM %q is on Docker Hub; the Railway builder cannot reach Docker Hub", ref)
	}
	i := strings.LastIndex(rest, ":")
	if i < 0 || i == len(rest)-1 {
		return "", fmt.Errorf("FROM %q carries no tag", ref)
	}
	return rest[i+1:], nil
}
