package main

import "errors"

// ParsePin returns the image tag from the single digest-pinned, non-Docker-Hub
// FROM line in dockerfile.
func ParsePin(dockerfile string) (string, error) {
	return "", errors.New("not implemented")
}
