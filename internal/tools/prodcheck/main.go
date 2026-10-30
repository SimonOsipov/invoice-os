// Command prodcheck reads production once and writes the prod_check section for nightlysummary.
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
