package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	stubKeyMarker    = "STUBPRIVATEKEYD"
	stubSecretMarker = "STUBJWTSECRETHEX"
	stubDSNPassword  = "stubadminpw"
)

// Stubs record each `docker run` as one line: argv, then the env values docker would forward by name.
var idpStubs = map[string]string{
	"docker": `#!/usr/bin/env bash
case "$1" in
  build) echo "sha256:stubimage"; echo "docker $*" >>"$STUB_LOG" ;;
  run)
    name=""; prev=""
    for a in "$@"; do [ "$prev" = "--name" ] && name="$a"; prev="$a"; done
    printf 'RUN %s ARGV %s\n' "$name" "$*" >>"$STUB_LOG"
    printf 'ENV %s DATABASE_URL=%s\n' "$name" "${DATABASE_URL:-}" >>"$STUB_LOG"
    printf 'ENV %s GOTRUE_JWT_KEYS=%s\n' "$name" "${GOTRUE_JWT_KEYS:-}" >>"$STUB_LOG"
    echo "0123456789abcdef" ;;
  inspect) echo true ;;
  *) echo "docker $*" >>"$STUB_LOG" ;;
esac
`,
	"go": `#!/usr/bin/env bash
echo "go: downloading example.com/noise v0.0.0"
out=""; prev=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done
cat >"$out" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  jwk-es256)
    n=$(( $(cat "$STUB_DIR/keycount" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$STUB_DIR/keycount"
    echo "[{\"kty\":\"EC\",\"kid\":\"k$n\",\"d\":\"STUBPRIVATEKEYD$n\"}]" ;;
  jwk-check) cat >/dev/null; echo "jwk-check: ok: one ES256 signing key" ;;
esac
EOF
chmod +x "$out"
`,
	"curl":    "#!/usr/bin/env bash\nexit 0\n",
	"openssl": "#!/usr/bin/env bash\necho " + stubSecretMarker + "\n",
	"sleep":   "#!/usr/bin/env bash\nexit 0\n",
}

type idpUpRun struct {
	stdout, stderr, log string
	code                int
}

// runIdpUp runs the real scripts/ci/idp-up.sh with docker, go, curl and openssl stubbed and uname fixed to osName.
func runIdpUp(t *testing.T, osName string, args ...string) idpUpRun {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stubs := map[string]string{"uname": "#!/usr/bin/env bash\necho " + osName + "\n"}
	for name, body := range idpStubs {
		stubs[name] = body
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(dir, "stub.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "ci", "idp-up.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = filepath.Join("..", "..")
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "GOTRUE_") && !strings.HasPrefix(kv, "DATABASE_URL=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "PATH="+bin+":"+os.Getenv("PATH"), "STUB_LOG="+logPath, "STUB_DIR="+dir)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run idp-up.sh: %v", err)
		}
		code = ee.ExitCode()
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return idpUpRun{out.String(), errb.String(), string(log), code}
}

func stubLines(log, prefix, name string) []string {
	var lines []string
	for _, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(l, prefix+" "+name+" ") {
			lines = append(lines, strings.TrimPrefix(l, prefix+" "+name+" "))
		}
	}
	return lines
}

func stubEnv(t *testing.T, log, container, name string) string {
	t.Helper()
	for _, l := range stubLines(log, "ENV", container) {
		if v, ok := strings.CutPrefix(l, name+"="); ok {
			return v
		}
	}
	t.Fatalf("the docker stub recorded no %s for %s; did the container start?", name, container)
	return ""
}

var idpContainers = []string{"idp-es256", "idp-hs256", "idp-rebuild"}

const idpIssuer = "urn:ascomply:auth:ci"

// argvEnv returns the value of `-e NAME=value` in a recorded docker argv, or "" when absent.
func argvEnv(argv, name string) string {
	fields := strings.Fields(argv)
	for i, f := range fields {
		if f == "-e" && i+1 < len(fields) {
			if v, ok := strings.CutPrefix(fields[i+1], name+"="); ok {
				return v
			}
		}
	}
	return ""
}

func TestIdpUpStdoutIsTheURLsAndTheIssuer(t *testing.T) {
	dsn := "postgres://supabase_auth_admin:" + stubDSNPassword + "@localhost:5448/invoice_os?sslmode=disable"
	for _, osName := range []string{"Linux", "Darwin"} {
		t.Run(osName, func(t *testing.T) {
			r := runIdpUp(t, osName, dsn, "5448")
			if r.code != 0 {
				t.Fatalf("exit %d; stderr=%s", r.code, r.stderr)
			}
			want := []string{
				"IDP_ES256_URL=http://localhost:9991", "IDP_HS256_URL=http://localhost:9992",
				"IDP_REBUILD_URL=http://localhost:9993", "IDP_MAIL_URL=http://localhost:9994",
				"MAILPIT_URL=http://localhost:8025", "IDP_ISSUER=" + idpIssuer,
			}
			got := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
			if !strings.HasSuffix(r.stdout, "\n") || !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
				t.Errorf("stdout = %q, want exactly these lines in any order: %q", r.stdout, want)
			}

			var printed string
			for _, l := range got {
				if v, ok := strings.CutPrefix(l, "IDP_ISSUER="); ok {
					printed = v
				}
			}
			if printed == "" {
				t.Error("stdout carries no IDP_ISSUER line")
			}
			for _, c := range idpContainers {
				runs := stubLines(r.log, "RUN", c)
				if len(runs) != 1 {
					t.Fatalf("%s started %d times, want 1", c, len(runs))
				}
				if passed := argvEnv(runs[0], "GOTRUE_JWT_ISSUER"); passed == "" || passed != printed {
					t.Errorf("%s: GOTRUE_JWT_ISSUER = %q, printed IDP_ISSUER = %q; they must be equal", c, passed, printed)
				}
			}
			if !strings.Contains(r.stderr, "jwk-check: ok") {
				t.Error("jwk-check's verdict is not on stderr; the harness did not run it")
			}
			for _, secret := range []string{stubKeyMarker, stubSecretMarker, stubDSNPassword} {
				if strings.Contains(r.stdout+r.stderr, secret) {
					t.Errorf("idp-up.sh output carries %s", secret)
				}
				for _, c := range idpContainers {
					for _, argv := range stubLines(r.log, "RUN", c) {
						if strings.Contains(argv, secret) {
							t.Errorf("%s: docker argv carries %s; secrets pass by name", c, secret)
						}
					}
				}
			}
		})
	}
}

func TestIdpUpContainerConfiguration(t *testing.T) {
	dsn := "postgres://supabase_auth_admin:" + stubDSNPassword + "@localhost:5448/invoice_os?sslmode=disable"
	for _, osName := range []string{"Linux", "Darwin"} {
		t.Run(osName, func(t *testing.T) {
			r := runIdpUp(t, osName, dsn, "5448")
			if r.code != 0 {
				t.Fatalf("exit %d; stderr=%s", r.code, r.stderr)
			}
			if !strings.Contains(r.log, "docker build -q -f sidecar/auth/Dockerfile -t idp:ci sidecar/auth") {
				t.Errorf("the committed Dockerfile was not built; stub log:\n%s", r.log)
			}
			ports := map[string]string{"idp-es256": "9991", "idp-hs256": "9992", "idp-rebuild": "9993"}
			for _, c := range idpContainers {
				runs := stubLines(r.log, "RUN", c)
				if len(runs) != 1 {
					t.Fatalf("%s started %d times, want 1", c, len(runs))
				}
				argv := runs[0]
				for _, want := range []string{
					"-e GOTRUE_JWT_ISSUER=urn:ascomply:auth:ci", "-e GOTRUE_DISABLE_SIGNUP=false",
					"-e GOTRUE_MAILER_AUTOCONFIRM=true", "-e GOTRUE_SMTP_HOST= ", "-e PORT=" + ports[c],
					"-e DATABASE_URL ", "-e GOTRUE_JWT_SECRET ",
				} {
					if !strings.Contains(argv+" ", want) {
						t.Errorf("%s: argv lacks %q: %s", c, want, argv)
					}
				}
				if strings.Contains(argv, "HOOK_CUSTOM_ACCESS_TOKEN_ENABLED") {
					t.Errorf("%s overrides the image's hook switch: %s", c, argv)
				}
				if osName == "Linux" {
					if !strings.Contains(argv, "--network host") || strings.Contains(argv, " -p ") {
						t.Errorf("%s on Linux: want --network host and no -p: %s", c, argv)
					}
				} else {
					if strings.Contains(argv, "--network") || !strings.Contains(argv, "-p "+ports[c]+":"+ports[c]) {
						t.Errorf("%s off Linux: want -p %s:%s and no --network: %s", c, ports[c], ports[c], argv)
					}
				}
				wantDSN := dsn
				if osName != "Linux" {
					wantDSN = strings.Replace(dsn, "@localhost:5448/", "@host.docker.internal:5448/", 1)
				}
				if got := stubEnv(t, r.log, c, "DATABASE_URL"); got != wantDSN {
					t.Errorf("%s: DATABASE_URL = %q, want %q", c, got, wantDSN)
				}
			}

			es := stubEnv(t, r.log, "idp-es256", "GOTRUE_JWT_KEYS")
			rb := stubEnv(t, r.log, "idp-rebuild", "GOTRUE_JWT_KEYS")
			if !strings.Contains(es, stubKeyMarker) || !strings.Contains(rb, stubKeyMarker) {
				t.Fatalf("es256 or rebuild got no generated key: %q / %q", es, rb)
			}
			if es == rb {
				t.Error("idp-rebuild reuses idp-es256's key; each container gets its own")
			}
			for c, want := range map[string]bool{"idp-es256": true, "idp-hs256": false, "idp-rebuild": true} {
				if got := strings.Contains(stubLines(r.log, "RUN", c)[0]+" ", "-e GOTRUE_JWT_KEYS "); got != want {
					t.Errorf("%s forwards GOTRUE_JWT_KEYS = %v, want %v", c, got, want)
				}
			}
			if rbArgv := stubLines(r.log, "RUN", "idp-rebuild")[0]; !strings.Contains(rbArgv, "GOTRUE_HOOK_CUSTOM_ACCESS_TOKEN_URI=pg-functions://postgres/public/test_rebuild_claims_hook") {
				t.Errorf("idp-rebuild does not point the hook at test_rebuild_claims_hook: %s", rbArgv)
			}
			for _, c := range []string{"idp-es256", "idp-hs256"} {
				if strings.Contains(stubLines(r.log, "RUN", c)[0], "HOOK_CUSTOM_ACCESS_TOKEN_URI") {
					t.Errorf("%s overrides the image's hook URI", c)
				}
			}
		})
	}
}

func TestIdpUpMailContainerConfiguration(t *testing.T) {
	dsn := "postgres://supabase_auth_admin:" + stubDSNPassword + "@localhost:5448/invoice_os?sslmode=disable"
	for _, osName := range []string{"Linux", "Darwin"} {
		t.Run(osName, func(t *testing.T) {
			r := runIdpUp(t, osName, dsn, "5448")
			if r.code != 0 {
				t.Fatalf("exit %d; stderr=%s", r.code, r.stderr)
			}
			mp := stubLines(r.log, "RUN", "mailpit")
			im := stubLines(r.log, "RUN", "idp-mail")
			if len(mp) != 1 || len(im) != 1 {
				t.Fatalf("mailpit started %d times, idp-mail %d times; want 1 each", len(mp), len(im))
			}
			if !regexp.MustCompile(` axllent/mailpit:v[0-9.]+@sha256:[0-9a-f]{64}$`).MatchString(mp[0]) {
				t.Errorf("mailpit image is not pinned by tag and digest: %s", mp[0])
			}

			smtpHost := "localhost"
			if osName != "Linux" {
				smtpHost = "host.docker.internal"
			}
			for name, want := range map[string]string{
				"GOTRUE_MAILER_AUTOCONFIRM":           "false",
				"GOTRUE_SMTP_HOST":                    smtpHost,
				"GOTRUE_SMTP_PORT":                    "1025",
				"GOTRUE_MAILER_URLPATHS_CONFIRMATION": "http://localhost:9995/auth/verify",
				"GOTRUE_DISABLE_SIGNUP":               "false",
				"PORT":                                "9994",
				"GOTRUE_JWT_ISSUER":                   idpIssuer,
			} {
				if got := argvEnv(im[0], name); got != want {
					t.Errorf("idp-mail %s = %q, want %q: %s", name, got, want, im[0])
				}
			}
			if strings.Contains(im[0], "HOOK_CUSTOM_ACCESS_TOKEN") {
				t.Errorf("idp-mail overrides the image's hook; AC-7 needs the committed one: %s", im[0])
			}
			if !strings.Contains(im[0]+" ", "-e GOTRUE_JWT_KEYS ") || !strings.Contains(stubEnv(t, r.log, "idp-mail", "GOTRUE_JWT_KEYS"), stubKeyMarker) {
				t.Errorf("idp-mail gets no ES256 key by name: %s", im[0])
			}
			for _, secret := range []string{stubKeyMarker, stubSecretMarker, stubDSNPassword} {
				if strings.Contains(im[0], secret) {
					t.Errorf("idp-mail argv carries %s", secret)
				}
			}

			if osName == "Linux" {
				for _, argv := range []string{mp[0], im[0]} {
					if !strings.Contains(argv, "--network host") || strings.Contains(argv, " -p ") {
						t.Errorf("on Linux: want --network host and no -p: %s", argv)
					}
				}
			} else {
				for _, want := range []string{"-p 1025:1025", "-p 8025:8025"} {
					if !strings.Contains(mp[0], want) || strings.Contains(mp[0], "--network") {
						t.Errorf("mailpit off Linux: want %s and no --network: %s", want, mp[0])
					}
				}
				if !strings.Contains(im[0], "-p 9994:9994") {
					t.Errorf("idp-mail off Linux: want -p 9994:9994: %s", im[0])
				}
			}

			// docker keeps the last -e of a name, so a second AUTOCONFIRM would silently win.
			for _, c := range append(slices.Clone(idpContainers), "idp-mail") {
				if n := strings.Count(stubLines(r.log, "RUN", c)[0], "-e GOTRUE_MAILER_AUTOCONFIRM="); n != 1 {
					t.Errorf("%s passes GOTRUE_MAILER_AUTOCONFIRM %d times, want 1", c, n)
				}
			}
		})
	}
}

func TestIdpDownRemovesEveryContainerIdpUpStarts(t *testing.T) {
	up := runIdpUp(t, "Linux", "postgres://supabase_auth_admin:pw@localhost:5448/invoice_os", "5448")
	var started []string
	for _, l := range strings.Split(up.log, "\n") {
		if rest, ok := strings.CutPrefix(l, "RUN "); ok {
			started = append(started, strings.Fields(rest)[0])
		}
	}
	if len(started) < 5 {
		t.Fatalf("control: idp-up.sh started %v; want at least five containers", started)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "stub.log")
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/usr/bin/env bash\necho \"$*\" >>\""+logPath+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "ci", "idp-down.sh"))
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("idp-down.sh: %v: %s", err, out)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	removed := strings.Fields(string(log))
	for _, c := range started {
		if !slices.Contains(removed, c) {
			t.Errorf("idp-down.sh leaves %s running; it ran: %s", c, log)
		}
	}
}

func TestIdpUpRefusesANonAuthAdminDSN(t *testing.T) {
	good := runIdpUp(t, "Linux", "postgres://supabase_auth_admin:pw@localhost:5448/invoice_os", "5448")
	if good.code != 0 || !strings.Contains(good.log, "RUN idp-es256") {
		t.Fatalf("control: the auth-admin DSN did not start the containers: exit %d, stderr=%s", good.code, good.stderr)
	}
	for _, c := range []struct{ name, dsn string }{
		{"superuser", "postgres://postgres:postgres@localhost:5448/invoice_os?sslmode=disable"},
		{"migrator", "postgres://invoice_migrator:migrator@localhost:5448/invoice_os"},
		{"no userinfo", "postgres://localhost:5448/invoice_os"},
		{"auth admin as a prefix of another role", "postgres://supabase_auth_admin_x:pw@localhost:5448/invoice_os"},
		{"empty", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runIdpUp(t, "Linux", c.dsn, "5448")
			if r.code == 0 {
				t.Errorf("idp-up.sh accepted %q", c.dsn)
			}
			if r.stdout != "" {
				t.Errorf("stdout = %q, want empty; CI appends it to $GITHUB_ENV", r.stdout)
			}
			if strings.Contains(r.log, "RUN ") || strings.Contains(r.log, "docker build") {
				t.Errorf("docker ran before the DSN was refused:\n%s", r.log)
			}
		})
	}
}

// plannedIdPFilter is the idp paths filter the plan lists; the job must run when any of them changes.
var plannedIdPFilter = []string{
	"sidecar/auth/**", "internal/platform/auth/**", "migrations/**", "db/**", "tools/prenv/**",
	"internal/gateway/**", "internal/tenancy/**", "internal/platform/*.go", "internal/platform/db/**",
	"internal/tools/idppin/**", "scripts/ci/idp-*.sh", "Makefile", ".github/workflows/ci.yml", "go.mod", "go.sum",
}

func TestIdPFilterListsEveryPlannedPath(t *testing.T) {
	const fixture = "jobs:\n  changes:\n    steps:\n      - uses: dorny/paths-filter@v3\n        with:\n          filters: |\n" +
		"            idp:\n              - 'db/**'\n              # - 'Makefile'\n            other:\n              - 'Makefile'\n"
	if got := filterPaths(jobBlock(yamlCode(fixture), "changes"), "idp"); !slices.Equal(got, []string{"db/**"}) {
		t.Fatalf("fixture: idp filter = %v, want [db/**]; a commented or sibling entry was counted", got)
	}

	paths := filterPaths(jobBlock(yamlCode(readCIYAML(t)), "changes"), "idp")
	if len(paths) == 0 {
		t.Fatal("found no idp paths filter in ci.yml; the scan is broken")
	}
	for _, want := range plannedIdPFilter {
		if !slices.Contains(paths, want) {
			t.Errorf("the idp paths filter %v does not list %q", paths, want)
		}
	}
	if got := fmt.Sprint(filterPaths(jobBlock(yamlCode(readCIYAML(t)), "changes"), "sidecar")); !strings.Contains(got, "sidecar/**") {
		t.Errorf("the docling sidecar filter %s lost sidecar/**", got)
	}
}
