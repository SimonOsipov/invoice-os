package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	sidecarDir    = "sidecar/docling/"
	sidecarTests  = sidecarDir + "tests/"
	cleanTag      = "docling:test"
	pytestSeconds = "600"
)

// Exec runs one command. A non-zero exit is a code, not an error; err means
// the command did not start or outlived its deadline.
type Exec func(dir, name string, args ...string) (out string, code int, err error)

var buildOnlyName = regexp.MustCompile(`^(requirements.*\.txt|.*\.lock)$`)

// BuildInput reports whether file, directly in sidecar/docling/, may shape the
// test image through a build step; only a rebuild delivers that mutation.
func BuildInput(file string) bool {
	return path.Dir(filepath.ToSlash(file))+"/" == sidecarDir
}

// buildOnly reports a build input that the /app mount cannot deliver at all.
func buildOnly(file string) bool {
	b := path.Base(filepath.ToSlash(file))
	return b == "Dockerfile" || b == "Dockerfile.dockerignore" || buildOnlyName.MatchString(b)
}

// Docker runs pytest rows in the docling:test image. Root is absolute.
type Docker struct {
	Exec Exec
	Root string

	prepared bool
	cleanID  string
	cleanErr error

	mu   sync.Mutex
	seq  int
	live string // name of the container a docker run is using now
}

// docker runs `docker <args>` in Root. Start errors and deadlines come back as errors.
func (d *Docker) docker(args ...string) (string, int, error) {
	out, code, err := d.Exec(d.Root, "docker", args...)
	if err != nil {
		return out, code, fmt.Errorf("docker %s %v%s", args[0], err, lastLinesSuffix(out))
	}
	return out, code, nil
}

func lastLinesSuffix(out string) string {
	if l := lastLines(out, 5); l != "" {
		return ": " + l
	}
	return ""
}

// imageID returns the sha256: ID that `docker build -q` printed.
func imageID(out string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	id := strings.TrimSpace(lines[len(lines)-1])
	return id, strings.HasPrefix(id, "sha256:")
}

const buildDockerfile = sidecarDir + "Dockerfile"

// clean probes Docker and builds the clean image once; the result, error
// included, is kept for the run.
func (d *Docker) clean() (string, error) {
	if d.prepared {
		return d.cleanID, d.cleanErr
	}
	d.prepared = true
	d.cleanID, d.cleanErr = d.probeAndBuild()
	return d.cleanID, d.cleanErr
}

func (d *Docker) probeAndBuild() (string, error) {
	out, code, err := d.Exec(d.Root, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil || code != 0 {
		why := lastLines(out, 5)
		if err != nil {
			why = strings.TrimSpace(fmt.Sprintf("%v %s", err, why))
		}
		return "", fmt.Errorf("Docker is not running or not installed: %s", why)
	}
	out, code, err = d.docker("build", "-q", "--target", "test", "-f", buildDockerfile, "-t", cleanTag, ".")
	if err != nil {
		return "", err
	}
	id, ok := imageID(out)
	if code != 0 || !ok {
		return "", fmt.Errorf("%s build failed: %s", cleanTag, lastLines(out, 5))
	}
	return id, nil
}

// Pytest runs the row's test in the clean image. For a mutated build-input
// file whose mounted run stays green, it rebuilds from the mutated tree and
// runs again, because a mount cannot deliver that mutation.
func (d *Docker) Pytest(r Row, mutated bool) (Outcome, error) {
	id, err := d.clean()
	if err != nil {
		return NotRun, err
	}
	out, err := d.runIn(id, r)
	if err != nil || !mutated || out != Passed || !BuildInput(r.File) {
		return out, err
	}
	out2, code, err := d.docker("build", "-q", "--target", "test", "-f", buildDockerfile, ".")
	if err != nil {
		return NotRun, err
	}
	newID, ok := imageID(out2)
	if code != 0 || !ok {
		return NotRun, fmt.Errorf("image build from the mutated tree failed: %s", lastLines(out2, 5))
	}
	if newID != id {
		defer d.removeImage(newID)
	}
	return d.runIn(newID, r)
}

// removeImage removes an image this run built, unless a tag points at it: a
// cached rebuild can return the ID of another tree's tagged image.
func (d *Docker) removeImage(id string) {
	out, code, err := d.docker("image", "inspect", "-f", "{{len .RepoTags}}", id)
	if err != nil || code != 0 || strings.TrimSpace(out) != "0" {
		return
	}
	out, code, err = d.docker("image", "rm", id)
	if err != nil || code != 0 {
		why := lastLines(out, 5)
		if err != nil {
			why = err.Error()
		}
		fmt.Fprintf(os.Stderr, "mutationreplay: WARNING image rm %s: %s\n", id, why)
	}
}

// runIn runs the row's test in image id. The /app mount puts the mutated
// file in front of the image's own copy.
func (d *Docker) runIn(id string, r Row) (Outcome, error) {
	name := d.begin()
	defer d.end()
	args := []string{"run", "--rm", "--name", name, "-v", d.Root + ":/repo:ro", "-e", "REPO_ROOT=/repo"}
	if f := filepath.ToSlash(r.File); strings.HasPrefix(f, sidecarDir) && !buildOnly(f) {
		args = append(args, "-v", filepath.Join(d.Root, r.File)+":/app/"+strings.TrimPrefix(f, sidecarDir)+":ro")
	}
	node := "tests/" + strings.TrimPrefix(filepath.ToSlash(r.TestFile), sidecarTests) + "::" + r.Test
	args = append(args, id, "timeout", pytestSeconds, "pytest", "-v", node)
	out, code, err := d.docker(args...)
	if err != nil || code < 0 {
		d.RemoveLive() // the deadline or a signal killed the docker CLI, not the container
	}
	if err != nil {
		return NotRun, err
	}
	if code == 124 {
		return NotRun, fmt.Errorf("pytest timed out after %ss", pytestSeconds)
	}
	if code != 0 && !strings.Contains(out, "test session starts") && !strings.Contains(out, "ImportError while loading conftest") {
		return NotRun, fmt.Errorf("docker run failed (exit %d): %s", code, lastLines(out, 5))
	}
	return PytestOutcome(out, r.Test), nil
}

func (d *Docker) begin() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	d.live = fmt.Sprintf("mutationreplay-%d-%d", os.Getpid(), d.seq)
	return d.live
}

func (d *Docker) end() {
	d.mu.Lock()
	d.live = ""
	d.mu.Unlock()
}

// RemoveLive force-removes the container of the run in flight, if any.
func (d *Docker) RemoveLive() {
	d.mu.Lock()
	name := d.live
	d.mu.Unlock()
	if name == "" {
		return
	}
	out, code, err := d.docker("rm", "-f", name)
	if err == nil && code != 0 && (strings.Contains(out, "already in progress") || strings.Contains(out, "No such container")) {
		return // --rm got there first
	}
	if err != nil || code != 0 {
		detail := lastLines(out, 3)
		if err != nil {
			detail = err.Error()
		}
		fmt.Fprintf(os.Stderr, "mutationreplay: WARNING rm -f %s: %s\n", name, detail)
	}
}

// Runners sends pytest rows to Docker and every other row to other. Only the
// mutated runner may rebuild.
func Runners(other Runner, d *Docker) (control, run Runner) {
	dispatch := func(mutated bool) Runner {
		return func(r Row, k Kind) (Outcome, error) {
			if k == KindPytest {
				return d.Pytest(r, mutated)
			}
			return other(r, k)
		}
	}
	return OnceControl(dispatch(false)), dispatch(true)
}
