package portforward

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/spf13/cobra"

	"github.com/iximiuz/cdebug/pkg/cmd"
	"github.com/iximiuz/cdebug/pkg/util"
)

// TODO:
//   - parse ports args
//   - handle non-default network case
//   - handle exposing localhost ports
//       cdebug exec --name helper --image socat <target> <target-port> <random-port>
//       cdebug port-forward helper <host-port>:<random-port>

const (
	helperImage = "nixery.dev/socat:latest"
)

type options struct {
	target  string
	address string
	ports   []string
}

func NewCommand(cli cmd.CLI) *cobra.Command {
	var opts options

	cmd := &cobra.Command{
		Use:   "port-forward [OPTIONS] CONTAINER [LOCAL_PORT:]REMOTE_PORT [...[LOCAL_PORT_N:]REMOTE_PORT_N]",
		Short: "Publish a port of an already running container (kind of)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.target = args[0]
			if len(args) > 1 {
				opts.ports = args[1:]
			}
			return runPortForward(context.Background(), cli, &opts)
		},
	}

	flags := cmd.Flags()
	flags.SetInterspersed(false) // Instead of relying on --

	flags.StringVar(
		&opts.address,
		"address",
		"127.0.0.1",
		"Host's interface address to bind the port to",
	)

	return cmd
}

func runPortForward(ctx context.Context, cli cmd.CLI, opts *options) error {
	client, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return fmt.Errorf("cannot initialize Docker client: %w", err)
	}

	target, err := client.ContainerInspect(ctx, opts.target)
	if err != nil {
		return fmt.Errorf("cannot inspect target container: %w", err)
	}

	if err := pullImage(ctx, cli, client, helperImage); err != nil {
		return err
	}

	ports, portBindings, err := nat.ParsePortSpecs([]string{"8080:80"})
	if err != nil {
		return err
	}

	contIP := target.NetworkSettings.Networks["bridge"].IPAddress
	resp, err := client.ContainerCreate(
		ctx,
		&container.Config{
			Image:      helperImage,
			Entrypoint: []string{"socat"},
			Cmd: []string{
				"TCP-LISTEN:80,fork",
				fmt.Sprintf("TCP-CONNECT:%s:%d", contIP, 80),
			},
			ExposedPorts: ports,
		},
		&container.HostConfig{
			AutoRemove:   true,
			PortBindings: portBindings,
		},
		nil,
		nil,
		"port-forwarder-"+util.ShortID(),
	)
	if err != nil {
		return fmt.Errorf("cannot create port-forwarder container: %w", err)
	}

	if err := client.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("cannot start port-forwarder container: %w", err)
	}

	forwarderStatusCh, forwarderErrCh := client.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	// targetStatusCh, targetErrCh := client.ContainerWait(ctx, target.ID, container.WaitConditionNotRunning)
	select {
	case err := <-forwarderErrCh:
		if err != nil {
			return fmt.Errorf("waiting port-forwarder container failed: %w", err)
		}
	case <-forwarderStatusCh:
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
		return fmt.Errorf("cannot pull port-forwarder helper image %q: %w", image, err)
	}
	defer resp.Close()

	_, err = io.Copy(cli.OutputStream(), resp)
	return err
}
