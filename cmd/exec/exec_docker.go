package exec

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/sirupsen/logrus"

	"github.com/iximiuz/cdebug/pkg/cliutil"
	"github.com/iximiuz/cdebug/pkg/docker"
	"github.com/iximiuz/cdebug/pkg/tty"
	"github.com/iximiuz/cdebug/pkg/uuid"
)

// debugImageExistsLocally checks if the debug image exists in the host and that it matches the target container
// platform.
func debugImageExistsLocally(ctx context.Context, client *docker.Client, debugImage string, debugImagePlatform string, target types.ContainerJSON) (bool, error) {
	debugImageInspect, _, err := client.ImageInspectWithRaw(ctx, debugImage)
	if err != nil {
		// err means that the requested image wasn't found,
		// therefore it needs to be pulled
		logrus.Debugf("The image %s wasn't found locally", debugImage)
		return false, nil
	}

	debugImageOs := debugImageInspect.Os
	debugImageArchitecture := debugImageInspect.Architecture
	// override through --platform flag
	if debugImagePlatform != "" {
		fmt.Sscanf(debugImagePlatform, "%s/%s", &debugImageOs, &debugImageArchitecture)
	}

	// target.Platform only contains the Os but doesn't have the Arch, however we can
	// get the Os & Arch by analyzing the image using target.Image
	targetImageInspect, _, err := client.ImageInspectWithRaw(ctx, target.Image)
	if err != nil {
		return false, fmt.Errorf("failed to inspect image %s: %v", target.Image, err)
	}

	// debug image exists, also check that the platform matches the target image platform
	if debugImageArchitecture != targetImageInspect.Architecture || debugImageOs != targetImageInspect.Os {
		return false, nil
	}

	return true, nil
}

func runDebuggerDocker(ctx context.Context, cli cliutil.CLI, opts *options) error {
	client, err := docker.NewClient(docker.Options{
		Out:  cli.AuxStream(),
		Host: opts.runtime,
	})
	if err != nil {
		return err
	}

	target, err := client.ContainerInspect(ctx, opts.target)
	if err != nil {
		return err
	}
	if target.State == nil || !target.State.Running {
		return errTargetNotRunning
	}

	imageExists, err := debugImageExistsLocally(ctx, client, opts.image, opts.platform, target)
	if err != nil {
		return err
	}

	if !imageExists {
		cli.PrintAux("Pulling debugger image...\n")
		if err := client.ImagePullEx(ctx, opts.image, types.ImagePullOptions{
			Platform: func() string {
				if len(opts.platform) == 0 {
					return target.Platform
				}
				return opts.platform
			}(),
		}); err != nil {
			return errCannotPull(opts.image, err)
		}
	}

	runID := uuid.ShortID()
	nsMode := "container:" + target.ID
	targetPID := 1
	if target.HostConfig.PidMode.IsHost() {
		targetPID = target.State.Pid
	}

	resp, err := client.ContainerCreate(
		ctx,
		&container.Config{
			Image:        opts.image,
			Entrypoint:   []string{"sh"},
			Cmd:          []string{"-c", debuggerEntrypoint(cli, runID, targetPID, opts.image, opts.cmd)},
			Tty:          opts.tty,
			OpenStdin:    opts.stdin,
			AttachStdin:  opts.stdin,
			AttachStdout: true,
			AttachStderr: true,
		},
		&container.HostConfig{
			Privileged: target.HostConfig.Privileged || opts.privileged,
			CapAdd:     target.HostConfig.CapAdd,
			CapDrop:    target.HostConfig.CapDrop,

			AutoRemove: opts.autoRemove,

			NetworkMode: container.NetworkMode(nsMode),
			PidMode:     container.PidMode(nsMode),
			UTSMode:     container.UTSMode(nsMode),
			// TODO: CgroupnsMode: container.CgroupnsMode(nsMode),
			// TODO: IpcMode:      container.IpcMode(nsMode)
			// TODO: UsernsMode:   container.UsernsMode(target)
		},
		nil,
		nil,
		debuggerName(opts.name, runID),
	)
	if err != nil {
		return errCannotCreate(err)
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

func attachDebugger(
	ctx context.Context,
	cli cliutil.CLI,
	client *docker.Client,
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
			logrus.Debugf("ioStreamer.stream() failed: %s", err)
		}
	}()

	return resp.Close, nil
}

type ioStreamer struct {
	streams cliutil.Streams

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
