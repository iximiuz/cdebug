package main

import (
	"fmt"
	"os"

	"github.com/moby/term"
	"github.com/spf13/cobra"

	"github.com/iximiuz/cdebug/cmd/exec"
	"github.com/iximiuz/cdebug/cmd/portforward"
	"github.com/iximiuz/cdebug/pkg/cmd"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	stdin, stdout, stderr := term.StdStreams()
	cli := cmd.NewCLI(stdin, stdout, stderr)

	cmd := &cobra.Command{
		Use:     "cdebug [OPTIONS] COMMAND [ARG...]",
		Short:   "The base command for the cdebug CLI.",
		Version: fmt.Sprintf("%s (built: %s commit: %s)", version, date, commit),
	}

	cmd.AddCommand(
		exec.NewCommand(cli),
		portforward.NewCommand(cli),
		// TODO: other commands
	)

	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(stderr, err.Error())
		os.Exit(1)
	}
}
