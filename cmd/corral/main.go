// Command corral runs AI coding agents inside isolated Lima VMs.
package main

import (
	"os"

	"github.com/corral-sh/corral/internal/cli"

	// Registered agents. Add new agents here — nothing else needs to change.
	_ "github.com/corral-sh/corral/internal/agent/claude"
)

func main() {
	os.Exit(cli.Execute())
}
