package exec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"text/template"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/iximiuz/cdebug/pkg/cmd"
	"github.com/iximiuz/cdebug/pkg/tty"
)

const (
	defaultToolkitImage = "docker.io/library/busybox:latest"
)

type options struct {
	target     string
	name       string
	image      string
	tty        bool
	stdin      bool
	cmd        []string
	privileged bool
	autoRemove bool
}

func NewCommand(cli cmd.CLI) *cobra.Command {
	var opts options

	cmd := &cobra.Command{
		Use:   "exec [OPTIONS] CONTAINER [COMMAND] [ARG...]",
		Short: "Start a debugger shell in the target container",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.target = args[0]
			if len(args) > 1 {
				opts.cmd = args[1:]
			}
			return runDebugger(context.Background(), cli, &opts)
		},
	}

	flags := cmd.Flags()
	flags.SetInterspersed(false) // Instead of relying on --

	flags.StringVar(
		&opts.name,
		"name",
		"",
		"Assign a name to the debugger container",
	)
	flags.StringVar(
		&opts.image,
		"image",
		defaultToolkitImage,
		"Debugging toolkit image (hint: use 'busybox' or 'nixery.dev/shell/tool1/tool2/etc...')",
	)
	flags.BoolVarP(
		&opts.stdin,
		"interactive",
		"i",
		false,
		"Keep the STDIN open (as in `docker exec -i`)",
	)
	flags.BoolVarP(
		&opts.tty,
		"tty",
		"t",
		false,
		"Allocate a pseudo-TTY (as in `docker exec -t`)",
	)
	flags.BoolVar(
		&opts.privileged,
		"privileged",
		false,
		"God mode for the debugger container (as in `docker run --privileged`)",
	)
	flags.BoolVar(
		&opts.autoRemove,
		"rm",
		false,
		"Automatically remove the debugger container when it exits (as in `docker run --rm`)",
	)

	return cmd
}

var (
	chrootEntrypoint = template.Must(template.New("chroot-entrypoint").Parse(`
set -euo pipefail

{{ if .IsNix }}
rm -rf /proc/1/root/nix
ln -s /proc/$$/root/nix /proc/1/root/nix
{{ end }}

ln -s /proc/$$/root/bin/ /proc/1/root/.cdebug-{{ .ID }}

cat > /.cdebug-entrypoint.sh <<EOF
#!/bin/sh
export PATH=$PATH:/.cdebug-{{ .ID }}

chroot /proc/1/root {{ .Cmd }}
EOF

sh /.cdebug-entrypoint.sh
`))
)

func runDebugger(ctx context.Context, cli cmd.CLI, opts *options) error {
	if err := cli.InputStream().CheckTty(opts.stdin, opts.tty); err != nil {
		return err
	}

	client, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return fmt.Errorf("cannot initialize Docker client: %w", err)
	}

	if err := pullImage(ctx, cli, client, opts.image); err != nil {
		return err
	}

	runID := shortID()
	target := "container:" + opts.target
	resp, err := client.ContainerCreate(
		ctx,
		&container.Config{
			Image:      opts.image,
			Entrypoint: []string{"sh"},
			Cmd: []string{
				"-c",
				mustRenderTemplate(
					chrootEntrypoint,
					map[string]any{
						"ID":    runID,
						"IsNix": strings.Contains(opts.image, "nixery"),
						"Cmd": func() string {
							if len(opts.cmd) == 0 {
								return "sh"
							}
							return "sh -c '" + strings.Join(shellescape(opts.cmd), " ") + "'"
						}(),
					},
				),
			},
			Tty:          opts.tty,
			OpenStdin:    opts.stdin,
			AttachStdin:  opts.stdin,
			AttachStdout: true,
			AttachStderr: true,
		},
		&container.HostConfig{
			Privileged: opts.privileged,
			AutoRemove: opts.autoRemove,

			NetworkMode: container.NetworkMode(target),
			PidMode:     container.PidMode(target),
			UTSMode:     container.UTSMode(target),
			// TODO: IpcMode:     container.IpcMode("container:my-distroless"),
		},
		nil,
		nil,
		debuggerName(opts.name, runID),
	)
	if err != nil {
		return fmt.Errorf("cannot create debugger container: %w", err)
	}

	close, err := attachDebugger(ctx, cli, client, opts, resp.ID)
	if err != nil {
		return fmt.Errorf("cannot attach to debugger container: %w", err)
	}
	defer close()

	if err := client.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("cannot start debugger container: %w", err)
	}

	if opts.tty && cli.OutputStream().IsTerminal() {
		tty.StartResizing(ctx, cli.OutputStream(), client, resp.ID)
	}

	statusCh, errCh := client.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("waiting debugger container failed: %w", err)
		}
	case <-statusCh:
	}

	return nil
}

func pullImage(
	ctx context.Context,
	cli cmd.CLI,
	client *dockerclient.Client,
	image string,
) error {
	resp, err := client.ImagePull(ctx, image, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("cannot pull debugger image %q: %w", image, err)
	}
	defer resp.Close()

	_, err = io.Copy(cli.OutputStream(), resp)
	return err
}

func attachDebugger(
	ctx context.Context,
	cli cmd.CLI,
	client *dockerclient.Client,
	opts *options,
	contID string,
) (func(), error) {
	resp, err := client.ContainerAttach(ctx, contID, types.ContainerAttachOptions{
		Stream: true,
		Stdin:  opts.stdin,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("cannot attach to debugger container: %w", err)
	}

	var cin io.ReadCloser
	if opts.stdin {
		cin = cli.InputStream()
	}

	var cout io.Writer = cli.OutputStream()
	var cerr io.Writer = cli.ErrorStream()
	if opts.tty {
		cerr = cli.OutputStream()
	}

	go func() {
		s := ioStreamer{
			streams:      cli,
			inputStream:  cin,
			outputStream: cout,
			errorStream:  cerr,
			resp:         resp,
			tty:          opts.tty,
			stdin:        opts.stdin,
		}

		if err := s.stream(ctx); err != nil {
			logrus.WithError(err).Warn("ioStreamer.stream() failed")
		}
	}()

	return resp.Close, nil
}

type ioStreamer struct {
	streams cmd.Streams

	inputStream  io.ReadCloser
	outputStream io.Writer
	errorStream  io.Writer

	resp types.HijackedResponse

	stdin bool
	tty   bool
}

func (s *ioStreamer) stream(ctx context.Context) error {
	if s.tty {
		s.streams.InputStream().SetRawTerminal()
		s.streams.OutputStream().SetRawTerminal()
		defer func() {
			s.streams.InputStream().RestoreTerminal()
			s.streams.OutputStream().RestoreTerminal()
		}()
	}

	inDone := make(chan error)
	go func() {
		if s.stdin {
			if _, err := io.Copy(s.resp.Conn, s.inputStream); err != nil {
				logrus.Debugf("Error forwarding stdin: %s", err)
			}
		}
		close(inDone)
	}()

	outDone := make(chan error)
	go func() {
		var err error
		if s.tty {
			_, err = io.Copy(s.outputStream, s.resp.Reader)
		} else {
			_, err = stdcopy.StdCopy(s.outputStream, s.errorStream, s.resp.Reader)
		}
		if err != nil {
			logrus.Debugf("Error forwarding stdout/stderr: %s", err)
		}
		close(outDone)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-inDone:
		<-outDone
		return nil
	case <-outDone:
		return nil
	}

	return nil
}

func debuggerName(name string, runID string) string {
	if len(name) > 0 {
		return name
	}
	return "cdebug-" + runID
}

func shortID() string {
	return strings.Split(uuid.NewString(), "-")[0]
}

func mustRenderTemplate(t *template.Template, data any) string {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		panic(fmt.Errorf("cannot render template %q: %w", t.Name(), err))
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
