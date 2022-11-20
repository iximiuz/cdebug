package main

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	mathrand "math/rand"
	"os"
	"time"

	"github.com/moby/term"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/iximiuz/cdebug/cmd/exec"
	"github.com/iximiuz/cdebug/cmd/portforward"
	"github.com/iximiuz/cdebug/pkg/cliutil"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func init() {
	var buf [8]byte
	if _, err := cryptorand.Read(buf[:]); err == nil {
		mathrand.Seed(int64(binary.LittleEndian.Uint64(buf[:])))
	} else {
		mathrand.Seed(time.Now().UnixNano())
	}
}

func main() {
	stdin, stdout, stderr := term.StdStreams()
	cli := cliutil.NewCLI(stdin, stdout, stderr)

	var logLevel string
	logrus.SetOutput(cli.ErrorStream())

	cmd := &cobra.Command{
		Use:     "cdebug [OPTIONS] COMMAND [ARG...]",
		Short:   "cdebug - a swiss army knife of container debugging",
		Version: fmt.Sprintf("%s (built: %s commit: %s)", version, date, commit),
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			setLogLevel(cli, logLevel)
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
		},
	}
	cmd.SetOut(cli.OutputStream())
	cmd.SetErr(cli.ErrorStream())

	cmd.AddCommand(
		exec.NewCommand(cli),
		portforward.NewCommand(cli),
		// TODO: other commands
	)

	flags := cmd.PersistentFlags()
	flags.SetInterspersed(false) // Instead of relying on --

	flags.StringVarP(
		&logLevel,
		"log-level",
		"l",
		"info",
		`log level for cdebug ("debug" | "info" | "warn" | "error" | "fatal")`,
	)

	if err := cmd.Execute(); err != nil {
		if sterr, ok := err.(cliutil.StatusError); ok {
			cli.PrintErr("cdebug: %s\n", sterr)
			os.Exit(sterr.Code())
		}

		// Hopefully, only usage errors.
		logrus.Debug("Exit error: %s", err)
		os.Exit(1)
	}
}

func setLogLevel(cli cliutil.CLI, logLevel string) {
	lvl, err := logrus.ParseLevel(logLevel)
	if err != nil {
		cli.PrintErr("Unable to parse log level: %s\n", logLevel)
		os.Exit(1)
	}
	logrus.SetLevel(lvl)
}
