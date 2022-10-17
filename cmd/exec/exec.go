package exec

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/iximiuz/cdebug/cmd"
)

// cdebug exec [--image busybox] <CONT> [CMD]
// cdebug exec [-it] --image nixery.dev/shell/ps <CONT> [CMD]

// cdebug images
//   - busybox
//   - alpine
//   - nixery:shell/ps

const (
	defaultToolkitImage = "docker.io/library/busybox:latest"
)

type options struct {
	target string
	name   string
	image  string
	tty    bool
	stdin  bool
	cmd    []string
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
		"Keep the STDIN open (same as in `docker exec -i`)",
	)
	flags.BoolVarP(
		&opts.tty,
		"tty",
		"t",
		false,
		"Allocate a pseudo-TTY (same as in `docker exec -t`)",
	)

	return cmd
}

const chrootProgramBusybox = `
set -euxo pipefail

rm -rf /proc/1/root/.cdebug
ln -s /proc/$$/root/bin/ /proc/1/root/.cdebug
export PATH=$PATH:/.cdebug
chroot /proc/1/root sh
`

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

	target := "container:" + opts.target
	resp, err := client.ContainerCreate(
		ctx,
		&container.Config{
			Image: opts.image,
			Cmd:   []string{"sh", "-c", chrootProgramBusybox},
			// AttachStdin: true,
			OpenStdin: opts.stdin,
			Tty:       opts.tty,
		},
		&container.HostConfig{
			NetworkMode: container.NetworkMode(target),
			PidMode:     container.PidMode(target),
			UTSMode:     container.UTSMode(target),
			// TODO: IpcMode:     container.IpcMode("container:my-distroless"),
		},
		nil,
		nil,
		debuggerName(opts.name),
	)
	if err != nil {
		return fmt.Errorf("cannot create debugger container: %w", err)
	}

	if opts.stdin || opts.tty {
		close, err := attachDebugger(ctx, cli, client, opts, resp.ID)
		if err != nil {
			return fmt.Errorf("cannot attach to debugger container: %w", err)
		}
		defer close()
	}

	if err := client.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("cannot start debugger container: %w", err)
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
	// TODO: check stdin && tty flags
	s.streams.InputStream().SetRawTerminal()
	s.streams.OutputStream().SetRawTerminal()
	defer func() {
		s.streams.InputStream().RestoreTerminal()
		s.streams.OutputStream().RestoreTerminal()
	}()

	inDone := make(chan error)
	go func() {
		io.Copy(s.resp.Conn, s.inputStream)
		close(inDone)
	}()

	outDone := make(chan error)
	go func() {
		io.Copy(s.outputStream, s.resp.Reader)
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

func debuggerName(name string) string {
	if len(name) > 0 {
		return name
	}

	return "cdebug-" + strings.Split(uuid.NewString(), "-")[0]
}
