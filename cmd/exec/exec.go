package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/distribution/reference"
	"github.com/spf13/cobra"

	"github.com/iximiuz/cdebug/pkg/cliutil"
)

const (
	defaultToolkitImage = "docker.io/library/busybox:musl"

	schemaContainerd = "containerd://"
	schemaDocker     = "docker://"
	schemaKubeCRI    = "cri://"
	schemaKubeLong   = "kubernetes://"
	schemaKubeShort  = "k8s://"
	schemaNerdctl    = "nerdctl://"
	schemaPodman     = "podman://"
	schemaOCI        = "oci://" // runc, crun, etc.

	exampleText = `
  # Start a %s shell in the Docker container:
  cdebug exec -it mycontainer
  cdebug exec -it docker://my-container

  # Execute a command in the Docker container:
  cdebug exec mycontainer cat /etc/os-release

  # Use a different debugging toolkit image:
  cdebug exec -it --image=alpine mycontainer

  # Use a nixery.dev image (https://nixery.dev/):
  cdebug exec -it --image=nixery.dev/shell/vim/ps/tshark mycontainer

  # Exec into a containerd container:
  cdebug exec -it containerd://mycontainer ...
  cdebug exec --namespace myns -it containerd://mycontainer ...

  # Exec into a nerdctl container:
  cdebug exec -it nerdctl://mycontainer ...

  # Start a shell in a Kubernetes pod:
  cdebug exec -it pod/mypod
  cdebug exec -it k8s://mypod
  cdebug exec --namespace=myns -it pod/mypod

  # Start a shell in a Kubernetes pod's container:
  cdebug exec -it pod/mypod/mycontainer`
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
	user       string
	privileged bool
	autoRemove bool
	quiet      bool

	runtime   string
	platform  string
	namespace string

	kubeconfig        string
	kubeconfigContext string
}

func NewCommand(cli cliutil.CLI) *cobra.Command {
	var opts options

	cmd := &cobra.Command{
		Use:     "exec [OPTIONS] [schema://][POD][CONTAINER] [COMMAND] [ARG...]",
		Short:   "Start a debugger shell in the target container or pod.",
		Example: fmt.Sprintf(exampleText[1:], strings.TrimPrefix(defaultToolkitImage, "docker.io/library/")),
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !opts.stdin {
				opts.quiet = true
			}
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
			} else if strings.HasPrefix(opts.target, "pod/") || strings.HasPrefix(opts.target, "pods/") {
				opts.schema = schemaKubeLong
			} else {
				opts.schema = schemaDocker
			}

			if !reference.ReferenceRegexp.MatchString(opts.image) {
				return cliutil.WrapStatusError(
					fmt.Errorf("invalid debugging toolkit image name %q: %v",
						opts.image, reference.ErrReferenceInvalidFormat),
				)
			}

			if opts.tty && !opts.stdin {
				return cliutil.WrapStatusError(errors.New("the -t/--tty flag requires the -i/--stdin flag"))
			}

			ctx := context.Background()

			switch opts.schema {
			case schemaContainerd, schemaNerdctl:
				return cliutil.WrapStatusError(runDebuggerContainerd(ctx, cli, &opts))

			case schemaDocker:
				return cliutil.WrapStatusError(runDebuggerDocker(ctx, cli, &opts))

			case schemaKubeLong, schemaKubeShort:
				return cliutil.WrapStatusError(runDebuggerKubernetes(ctx, cli, &opts))

			case schemaPodman, schemaOCI, schemaKubeCRI:
				return cliutil.WrapStatusError(errors.New("coming soon"))

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
		`Debugging toolkit image (hint: use "busybox:musl" or "nixery.dev/shell/vim/ps/tool3/tool4/...")`,
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
	flags.StringVarP(
		&opts.user,
		"user",
		"u",
		"",
		`Run the debugger container as User (format: <name|uid>[:<group|gid>])`,
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
	flags.StringVar(
		&opts.platform,
		"platform",
		"",
		`Platform (e.g., linux/amd64, linux/arm64) of the target container (for some runtimes it's hard to detect it automatically, but the debug sidecar must be of the same platform as the target)`,
	)
	flags.StringVar(
		&opts.kubeconfig,
		"kubeconfig",
		"",
		`Path to the kubeconfig file (default is $HOME/.kube/config)`,
	)
	flags.StringVar(
		&opts.kubeconfigContext,
		"kubeconfig-context",
		"",
		`Name of the kubeconfig context to use`,
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
	simpleEntrypoint = template.Must(template.New("user-entrypoint").Parse(`
set -euo pipefail

if [ "${HOME:-/}" != "/" ]; then
	ln -s /proc/{{ .TARGET_PID }}/root/ ${HOME}target-rootfs
fi

# TODO: Add target container's PATH to the user's PATH

exec {{ .Cmd }}
`))

	chrootEntrypoint = template.Must(template.New("chroot-entrypoint").Parse(`
set -euo pipefail

CURRENT_PID=$(sh -c 'echo $PPID')

{{ if .IsNix }}
CURRENT_NIX_INODE=$(stat -c '%i' /nix)
TARGET_NIX_INODE=$(stat -c '%i' /proc/{{ .TARGET_PID }}/root/nix 2>/dev/null || echo 0)
if [ ${CURRENT_NIX_INODE} -ne ${TARGET_NIX_INODE} ]; then
  rm -rf /proc/{{ .TARGET_PID }}/root/nix
  ln -s /proc/${CURRENT_PID}/root/nix /proc/{{ .TARGET_PID }}/root/nix
fi
{{ end }}

ln -s /proc/${CURRENT_PID}/root/bin/ /proc/{{ .TARGET_PID }}/root/.cdebug-{{ .ID }}

cat > /.cdebug-entrypoint.sh <<EOF
#!/bin/sh
export PATH=$PATH:/.cdebug-{{ .ID }}

chroot /proc/{{ .TARGET_PID }}/root {{ .Cmd }}
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
	chroot bool,
) string {
	if chroot {
		return mustRenderTemplate(
			cli,
			chrootEntrypoint,
			map[string]any{
				"ID":         runID,
				"TARGET_PID": targetPID,
				"IsNix":      strings.Contains(image, "nixery"),
				"Cmd": func() string {
					if len(cmd) == 0 {
						return ""
					}
					return "sh -c '" + strings.Join(shellescape(cmd), " ") + "'"
				}(),
			},
		)
	}

	return mustRenderTemplate(
		cli,
		simpleEntrypoint,
		map[string]any{
			"PID": targetPID,
			"Cmd": func() string {
				if len(cmd) == 0 {
					return "sh"
				}
				return strings.Join(shellescape(cmd), " ")
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

func isRootUser(user string) bool {
	return len(user) == 0 || user == "root" || user == "0" || user == "0:0"
}
