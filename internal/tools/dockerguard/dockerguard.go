package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	pinned  = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	varName = regexp.MustCompile(`\$(?:\{[^}]*\}|\w+)`)

	errNoDocker = errors.New("no package-ecosystem: docker entry")
)

// instructions drops comments and blank lines, joins continuations, and keeps each
// instruction's first line number.
func instructions(data []byte) (lines []string, nums []int) {
	var cur strings.Builder
	start := 0
	for i, l := range strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if cur.Len() == 0 {
			start = i + 1
		}
		if strings.HasSuffix(t, `\`) {
			cur.WriteString(strings.TrimSuffix(t, `\`) + " ")
			continue
		}
		cur.WriteString(t)
		lines, nums = append(lines, cur.String()), append(nums, start)
		cur.Reset()
	}
	if cur.Len() > 0 {
		lines, nums = append(lines, cur.String()), append(nums, start)
	}
	return lines, nums
}

// scan reports one Dockerfile's problems and its count of external FROM lines; path is repo-relative.
func scan(path string, data []byte) (problems []string, external int) {
	stages := map[string]bool{}
	seen := false
	lines, nums := instructions(data)
	for i, ins := range lines {
		f := strings.Fields(ins)
		if !strings.EqualFold(f[0], "FROM") {
			continue
		}
		seen = true
		f = f[1:]
		for len(f) > 0 && strings.HasPrefix(f[0], "--") {
			f = f[1:]
		}
		if len(f) == 0 {
			problems = append(problems, fmt.Sprintf("%s:%d: FROM names no image", path, nums[i]))
			continue
		}
		img := f[0]
		if !stages[strings.ToLower(img)] && !strings.EqualFold(img, "scratch") {
			switch {
			case strings.Contains(img, "$"):
				external++
				v := varName.FindString(img)
				if v == "" {
					v = img
				}
				problems = append(problems, fmt.Sprintf("%s:%d: FROM names its image through a variable (%s) — write the image with its @sha256 digest.", path, nums[i], v))
			case !pinned.MatchString(img):
				external++
				problems = append(problems, fmt.Sprintf("%s:%d: FROM %s carries no @sha256 digest — append @sha256:<64 hex>.", path, nums[i], img))
			default:
				external++
			}
		}
		if len(f) >= 3 && strings.EqualFold(f[1], "AS") {
			stages[strings.ToLower(f[2])] = true
		}
	}
	if !seen {
		problems = append(problems, path+": no FROM line")
	}
	return problems, external
}

// dockerDirs returns the docker entry's directories from dependabot.yml.
func dockerDirs(data []byte) ([]string, error) {
	var cfg struct {
		Updates []struct {
			Ecosystem   string   `yaml:"package-ecosystem"`
			Directory   string   `yaml:"directory"`
			Directories []string `yaml:"directories"`
		} `yaml:"updates"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	var dirs []string
	found := false
	for _, u := range cfg.Updates {
		if u.Ecosystem != "docker" {
			continue
		}
		found = true
		for _, d := range append(u.Directories, u.Directory) {
			if d != "" {
				dirs = append(dirs, path.Clean("/"+d))
			}
		}
	}
	if !found {
		return nil, errNoDocker
	}
	return dirs, nil
}

func dockerfiles(dir string) ([]string, error) {
	cmd := exec.Command("git", "-C", dir, "ls-files", "-z", "--",
		":(glob)**/Dockerfile", ":(glob)**/Dockerfile.*", ":(glob)**/*.Dockerfile",
		":(glob)**/*.dockerfile", ":(glob)**/Containerfile")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 && !strings.HasSuffix(string(p), ".dockerignore") {
			files = append(files, string(p))
		}
	}
	return files, nil
}

func run(dir string, stdout, stderr io.Writer) int {
	files, err := dockerfiles(dir)
	if err != nil {
		fmt.Fprintf(stderr, "dockerguard: git ls-files: %v\n", err)
		return 2
	}
	if len(files) == 0 {
		fmt.Fprintln(stderr, "dockerguard: no tracked Dockerfile")
		return 2
	}
	var problems []string
	external := 0
	have := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			fmt.Fprintf(stderr, "dockerguard: %v\n", err)
			return 2
		}
		p, n := scan(f, data)
		problems, external = append(problems, p...), external+n
		have[path.Clean("/"+path.Dir(f))] = true
	}
	const dep = ".github/dependabot.yml"
	data, err := os.ReadFile(filepath.Join(dir, dep))
	if err != nil {
		fmt.Fprintf(stderr, "dockerguard: %v\n", err)
		return 2
	}
	dirs, err := dockerDirs(data)
	switch {
	case errors.Is(err, errNoDocker):
		problems = append(problems, dep+": "+err.Error())
	case err != nil:
		fmt.Fprintf(stderr, "dockerguard: %s: %v\n", dep, err)
		return 2
	default:
		listed := map[string]bool{}
		for _, d := range dirs {
			listed[d] = true
		}
		for _, d := range sorted(have) {
			if !listed[d] {
				problems = append(problems, fmt.Sprintf("%s: the docker entry lacks %s, which holds a Dockerfile — add it.", dep, d))
			}
		}
		for _, d := range sorted(listed) {
			if !have[d] {
				problems = append(problems, fmt.Sprintf("%s: the docker entry lists %s, which holds no Dockerfile — remove it.", dep, d))
			}
		}
	}
	if len(problems) > 0 {
		fmt.Fprintln(stderr, strings.Join(problems, "\n"))
		return 1
	}
	fmt.Fprintf(stdout, "dockerguard: %d Dockerfiles, %d external FROM lines, all pinned\n", len(files), external)
	return 0
}

func sorted(m map[string]bool) []string {
	s := make([]string, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}
