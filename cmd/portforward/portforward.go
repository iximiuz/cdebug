package portforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/iximiuz/cdebug/pkg/cmd"
	"github.com/iximiuz/cdebug/pkg/util"
)

// TODO:
//   - parse ports args
//   - handle non-default network case
//   - handle exposing localhost ports
//       cdebug exec --name helper --image socat <target> <target-port> <proxy-port>
//       cdebug port-forward helper <host-port>:<proxy-port>

// Possible options (kinda sorta as in ssh -L):
//   - TARGET_PORT                                # binds TARGET_IP:TARGET_PORT to a random port on localhost
//   - TARGET_IP:TARGET_PORT                      # The second form is needed to:
//                                                #  1) allow exposing target's localhost ports
//                                                #  2) specify a concrete IP for a multi-network target
//
//   - LOCAL_PORT:TARGET_PORT                     # binds TARGET_IP:TARGET_PORT to LOCAL_PORT on localhost
//   - LOCAL_PORT:TARGET_IP:TARGET_PORT
//
//   - LOCAL_IP:LOCAL_PORT:TARGET_PORT            # similar to LOCAL_PORT:TARGET_PORT but LOCAL_IP is used instead of localhost
//   - LOCAL_IP:LOCAL_PORT:TARGET_IP:TARGET_PORT

const (
	helperImage = "nixery.dev/shell/socat:latest"
)

type options struct {
	target      string
	forwardings []string
}

func NewCommand(cli cmd.CLI) *cobra.Command {
	var opts options

	cmd := &cobra.Command{
		Use:   "port-forward CONTAINER [[LOCAL_IP:]LOCAL_PORT:]TARGET_PORT [...]",
		Short: `"Publish" one or more ports of an already running container`,
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.target = args[0]
			opts.forwardings = args[1:]
			return runPortForward(context.Background(), cli, &opts)
		},
	}

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

	forwardings, err := parseForwardings(target, opts.forwardings)
	if err != nil {
		return err
	}

	exposedPorts, portBindings, err := nat.ParsePortSpecs(forwardings.toDockerPortSpecs())
	if err != nil {
		return err
	}

	fmt.Println("forwardings")
	util.PrettyPrint(forwardings)

	fmt.Println("forwardings.toDockerPortSpecs()")
	util.PrettyPrint(forwardings.toDockerPortSpecs())

	fmt.Println("exposedPorts")
	util.PrettyPrint(exposedPorts)

	fmt.Println("portBindings")
	util.PrettyPrint(portBindings)

	// TODO: Iterate over all forwardings.
	resp, err := client.ContainerCreate(
		ctx,
		&container.Config{
			Image:      helperImage,
			Entrypoint: []string{"socat"},
			Cmd: []string{
				fmt.Sprintf("TCP-LISTEN:%s,fork", forwardings[0].targetPort),
				fmt.Sprintf("TCP-CONNECT:%s:%s", forwardings[0].targetIP, forwardings[0].targetPort),
			},
			ExposedPorts: exposedPorts,
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

	forwarder, err := client.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return fmt.Errorf("cannot inspect forwarder container: %w", err)
	}
	// TODO: Multi-network support.
	targetIP := target.NetworkSettings.Networks["bridge"].IPAddress
	for from, frontends := range forwarder.NetworkSettings.Ports {
		for _, frontend := range frontends {
			fmt.Printf("Forwarding %s to %s's %s:%s\n", net.JoinHostPort(frontend.HostIP, frontend.HostPort), target.Name[1:], targetIP, from)
		}
	}

	sigCh := make(chan os.Signal, 128)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer close(sigCh)

	go func() {
		for _ = range sigCh {
			fmt.Println("Exiting...")
			if err := client.ContainerKill(ctx, resp.ID, "KILL"); err != nil {
				logrus.WithError(err).Warn("Cannot kill forwarder container")
			}
			break
		}
	}()

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

type forwarding struct {
	localIP    string
	localPort  string
	targetIP   string
	targetPort string
}

type forwardingList []forwarding

func (list forwardingList) toDockerPortSpecs() []string {
	// ip:hostPort:containerPort | ip::containerPort | hostPort:containerPort | containerPort
	var spec []string
	for _, f := range list {
		spec = append(spec, fmt.Sprintf("%s:%s:%s", f.localIP, f.localPort, f.targetPort))
	}
	return spec
}

func parseForwardings(
	target types.ContainerJSON,
	forwardings []string,
) (forwardingList, error) {
	var list forwardingList

	targetIP := target.NetworkSettings.Networks["bridge"].IPAddress

	for _, f := range forwardings {
		parts := strings.Split(f, ":")
		if len(parts) == 1 {
			// Case 1: TARGET_PORT

			if _, err := nat.ParsePort(parts[0]); err != nil {
				// TODO: Return a user-friendly error.
				return nil, err
			}

			// TODO: if "target has more than 1 IP" return err

			list = append(list, forwarding{
				localIP:    "127.0.0.1",
				targetPort: parts[0],
				targetIP:   targetIP,
			})
			continue
		}

		if len(parts) == 2 {
			if _, err := nat.ParsePort(parts[0]); err == nil {
				// Case 2: LOCAL_PORT:TARGET_PORT

				// TODO: if "target has more than 1 IP" return err

				list = append(list, forwarding{
					localPort:  parts[0],
					localIP:    "127.0.0.1",
					targetPort: parts[1],
					targetIP:   targetIP,
				})
			} else {
				// Case 3: TARGET_IP:TARGET_PORT

				// TODO: if "parts[0] not in target IP list" return err

				list = append(list, forwarding{
					localIP:    "127.0.0.1",
					targetPort: parts[1],
					targetIP:   parts[0],
				})
			}
			continue
		}

		// TODO: other cases
		return nil, errors.New("implement me")
	}

	return list, nil
}
