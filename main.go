package main

import (
	"context"
	"io"
	"os"

	clistreams "github.com/docker/cli/cli/streams"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/moby/term"
	"github.com/sirupsen/logrus"
)

// cdebug exec [--image busybox] <CONT> [CMD]
// cdebug exec [-it] --image nixery.dev/shell/ps <CONT> [CMD]

// cdebug images
//   - busybox
//   - alpine
//   - nixery:shell/ps

const chrootProgram = `
set -euxo pipefail

rm -rf /proc/1/root/.cdebug
ln -s /proc/$$/root/bin/ /proc/1/root/.cdebug
export PATH=$PATH:/.cdebug
chroot /proc/1/root sh
`

type Streams struct {
	In  *clistreams.In
	Out *clistreams.Out
	Err io.Writer
}

func main() {
	stdin, stdout, stderr := term.StdStreams()
	streams := &Streams{
		In:  clistreams.NewIn(stdin),
		Out: clistreams.NewOut(stdout),
		Err: stderr,
	}

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}

	reader, err := cli.ImagePull(ctx, "docker.io/library/busybox", types.ImagePullOptions{})
	if err != nil {
		panic(err)
	}

	io.Copy(os.Stdout, reader)
	reader.Close()

	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: "busybox",
		Cmd:   []string{"sh", "-c", chrootProgram},
		// AttachStdin: true,
		OpenStdin: true,
		Tty:       true,
	}, &container.HostConfig{
		NetworkMode: container.NetworkMode("container:my-distroless"),
		PidMode:     container.PidMode("container:my-distroless"),
		// IpcMode:     container.IpcMode("container:my-distroless"),
		UTSMode: container.UTSMode("container:my-distroless"),
	}, nil, nil, "")
	if err != nil {
		panic(err)
	}

	close, err := attachContainer(ctx, cli, streams, resp.ID)
	if err != nil {
		panic(err)
	}
	defer close()

	if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		panic(err)
	}

	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			panic(err)
		}
	case <-statusCh:
	}
}

func attachContainer(ctx context.Context, cli *client.Client, streams *Streams, contID string) (func(), error) {
	options := types.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	}

	resp, err := cli.ContainerAttach(ctx, contID, options)
	if err != nil {
		return nil, err
	}

	var (
		out  io.Writer     = os.Stdout
		cerr io.Writer     = os.Stdout
		in   io.ReadCloser = os.Stdin
	)

	go func() {
		s := ioStreamer{
			streams:      streams,
			inputStream:  in,
			outputStream: out,
			errorStream:  cerr,
			resp:         resp,
		}

		if err := s.stream(ctx); err != nil {
			logrus.WithError(err).Warn("ioStreamer.stream() failed")
		}
	}()

	return resp.Close, nil
}

type ioStreamer struct {
	streams *Streams

	inputStream  io.ReadCloser
	outputStream io.Writer
	errorStream  io.Writer

	resp types.HijackedResponse
}

func (s *ioStreamer) stream(ctx context.Context) error {
	s.streams.In.SetRawTerminal()
	s.streams.Out.SetRawTerminal()
	defer func() {
		s.streams.In.RestoreTerminal()
		s.streams.Out.RestoreTerminal()
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
