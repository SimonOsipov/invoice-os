// Command prodidgate fails when a tracked file holds a production id.
//
//	go run ./internal/tools/prodidgate             scan every tracked file
//	go run ./internal/tools/prodidgate -selfcheck  prove the pinned digests match the Actions variables
//
// Exit 0 is clean, 1 is a finding or a mismatch, 2 is a listing or read error.
package main

import (
	"flag"
	"os"
)

func main() {
	self := flag.Bool("selfcheck", false, "compare the pinned digests with GCP_WIF_PROVIDER and HUBSPOT_PORTAL_ID")
	flag.Parse()
	if *self {
		os.Exit(selfcheck(os.Getenv, pinned, os.Stdout, os.Stderr))
	}
	os.Exit(run(".", pinned, os.Stdout, os.Stderr))
}
