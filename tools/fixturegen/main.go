// main.go is a no-op placeholder so `package main` satisfies `go build`
// (CI's `go build ./...`, .github/workflows/ci.yml:75/476) while gen.go is
// still panic-stubbed. The executor (Stage 3) replaces this with the real
// `--seed`/`--invoices`/`--out` CLI.
package main

func main() {}
