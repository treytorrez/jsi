// jsi — Just Send It: serverless-first P2P file transfer (PLAN.md §7.5,
// milestone M3). All logic lives in cmd/jsi/internal/cli; main is the
// thinnest possible shell so the flows stay testable.
package main

import (
	"os"

	"github.com/treyt/jsi/cmd/jsi/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args))
}
