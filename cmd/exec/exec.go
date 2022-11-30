package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/spf13/cobra"

	"github.com/iximiuz/cdebug/pkg/cliutil"
)

const (
	defaultToolkitImage = "docker.io/library/busybox:latest"

	schemaContainerd = "containerd://"
	schemaDocker     = "docker://"
	schemaKubeCRI    = "cri://"
	schemaKubeLong   = "kubernetes://"
	schemaKubeShort  = "k8s://"
	schemaNerdctl    = "nerdctl://"
)

var (
	errTargetNotFound = errors.New("target container not found")

	errTargetNotRunning = errors.New("target container found but it's not running: executing commands in stopped containers is not supported yet")
)

func errCannotPull(image string, cause error) error {
	return fmt.Errorf("cannot pull debugger image %q: %w", image, cause)
}

func errCannotCreate(cause error) error {
	return fmt.Errorf("cannot create debugger container: %w", cause)
}

type options struct {
	target     string
	schema     string
	name       string
	image      string
	tty        bool
	stdin      bool
	cmd        []string
	privileged bool
	autoRemove bool
	quiet      bool

	runtime   string
	namespace string
}

func NewCommand(cli cliutil.CLI) *cobra.Command {
	var opts options

	cmd := &cobra.Command{
		Use:   "exec [OPTIONS] CONTAINER [COMMAND] [ARG...]",
		Short: "Start a debugger shell in the target container",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli.SetQuiet(opts.quiet)

			if err := cli.InputStream().CheckTty(opts.stdin, opts.tty); err != nil {
				return cliutil.WrapStatusError(err)
			}

			opts.target = args[0]
			if len(args) > 1 {
				opts.cmd = args[1:]
			}

			if sep := strings.Index(opts.target, "://"); sep != -1 {
				opts.schema = opts.target[:sep+3]
				opts.target = opts.target[sep+3:]
			} else {
				opts.schema = schemaDocker
			}

			ctx := context.Background()

			switch opts.schema {
			case schemaContainerd, schemaNerdctl:
				return cliutil.WrapStatusError(runDebuggerContainerd(ctx, cli, &opts))

			case schemaDocker:
				return cliutil.WrapStatusError(runDebuggerDocker(ctx, cli, &opts))

			case schemaKubeCRI, schemaKubeLong, schemaKubeShort:
				return cliutil.WrapStatusError(errors.New("coming soon..."))

			default:
				return cliutil.WrapStatusError(fmt.Errorf("unknown schema %q", opts.schema))
			}
		},
	}

	flags := cmd.Flags()
	flags.SetInterspersed(false) // Instead of relying on --

	flags.BoolVarP(
		&opts.quiet,
		"quiet",
		"q",
		false,
		`Suppress verbose output`,
	)
	flags.StringVar(
		&opts.name,
		"name",
		"",
		`Assign a name to the debugger container`,
	)
	flags.StringVar(
		&opts.image,
		"image",
		defaultToolkitImage,
		`Debugging toolkit image (hint: use "busybox" or "nixery.dev/shell/vim/ps/tool3/tool4/...")`,
	)
	flags.BoolVarP(
		&opts.stdin,
		"interactive",
		"i",
		false,
		`Keep the STDIN open (as in "docker exec -i")`,
	)
	flags.BoolVarP(
		&opts.tty,
		"tty",
		"t",
		false,
		`Allocate a pseudo-TTY (as in "docker exec -t")`,
	)
	flags.BoolVar(
		&opts.privileged,
		"privileged",
		false,
		`God mode for the debugger container (as in "docker run --privileged")`,
	)
	flags.BoolVar(
		&opts.autoRemove,
		"rm",
		false,
		`Automatically remove the debugger container when it exits (as in "docker run --rm")`,
	)
	flags.StringVarP(
		&opts.namespace,
		"namespace",
		"n",
		"",
		`Namespace (the final meaning of this parameter is runtime specific)`,
	)
	flags.StringVar(
		&opts.runtime,
		"runtime",
		"",
		`Runtime address ("/var/run/docker.sock" | "/run/containerd/containerd.sock" | "https://<kube-api-addr>:8433/...)`,
	)

	return cmd
}

func debuggerName(name string, runID string) string {
	if len(name) > 0 {
		return name
	}
	return "cdebug-" + runID
}

var (
	chrootEntrypoint = template.Must(template.New("chroot-entrypoint").Parse(`
set -euo pipefail

{{ if .IsNix }}
rm -rf /proc/{{ .PID }}/root/nix
ln -s /proc/$$/root/nix /proc/{{ .PID }}/root/nix
{{ end }}

ln -s /proc/$$/root/bin/ /proc/{{ .PID }}/root/.cdebug-{{ .ID }}

cat > /.cdebug-entrypoint.sh <<EOF
#!/bin/sh
export PATH=$PATH:/.cdebug-{{ .ID }}

chroot /proc/{{ .PID }}/root {{ .Cmd }}
EOF

exec sh /.cdebug-entrypoint.sh
`))
)

func debuggerEntrypoint(
	cli cliutil.CLI,
	runID string,
	targetPID int,
	image string,
	cmd []string,
) string {
	return mustRenderTemplate(
		cli,
		chrootEntrypoint,
		map[string]any{
			"ID":    runID,
			"PID":   targetPID,
			"IsNix": strings.Contains(image, "nixery"),
			"Cmd": func() string {
				if len(cmd) == 0 {
					// bash provides a much better UX out of the box, so
					// let's try to use bash if we know it's likely available.
					if strings.Contains(image, "busybox") {
						return "bash"
					}
					if strings.HasPrefix(image, "nixery.dev") && strings.Contains(image, "/shell") {
						return "bash"
					}

					// Default to sh otherwise.
					return "sh"
				}
				return "sh -c '" + strings.Join(shellescape(cmd), " ") + "'"
			}(),
		},
	)
}

func mustRenderTemplate(cli cliutil.CLI, t *template.Template, data any) string {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		cli.PrintErr("Cannot render template %q: %w", t.Name(), err)
		os.Exit(1)
	}
	return buf.String()
}

// FIXME: Too naive. This will break for args containing escaped symbols.
func shellescape(args []string) (escaped []string) {
	for _, a := range args {
		if strings.ContainsAny(a, " \t\n\r") {
			a = `"` + a + `"`
		}
		escaped = append(escaped, a)
	}
	return
}
