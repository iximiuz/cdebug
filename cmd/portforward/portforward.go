package portforward

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/iximiuz/cdebug/pkg/cliutil"
	"github.com/iximiuz/cdebug/pkg/docker"
	"github.com/iximiuz/cdebug/pkg/signalutil"
	"github.com/iximiuz/cdebug/pkg/uuid"
)

// TODO:
//   - Local port forwarding: handle exposing localhost ports
//       cdebug exec --name helper --image socat <target> <target-port> <proxy-port>
//       cdebug port-forward helper <host-port>:<proxy-port>
//
//   - Remote port forwarding: implement me!
//
// Local port forwarding's possible modes (kinda sorta as in ssh -L):
//   - REMOTE_PORT                                # binds TARGET_IP:REMOTE_PORT to a random port on localhost
//   - REMOTE_<IP|ALIAS|NET>:REMOTE_PORT          # binds arbitrary REMOTE_ID:REMOTE_PORT to a random port on localhost
//                                                #  1) allows exposing target's localhost ports
//                                                #  2) allows specifyng a concrete IP for a multi-network target
//                                                #  3) allows specifyng an arbitrary destination reachable from the target
//
//   - LOCAL_PORT:REMOTE_PORT                     # much like the above form but uses a concrete port on the host system
//   - LOCAL_PORT:REMOTE_<IP|ALIAS|NET>:REMOTE_PORT
//
//   - LOCAL_HOST:LOCAL_PORT:REMOTE_PORT          # similar to LOCAL_PORT:REMOTE_PORT but LOCAL_HOST is used instead of 127.0.0.1
//   - LOCAL_HOST:LOCAL_PORT:REMOTE_<IP|ALIAS|NET>:REMOTE_PORT
//
// Remote port forwarding's possible modes (kinda sorta as in ssh -R):
//   - coming soon...

const (
	forwarderImage = "nixery.dev/shell/socat:latest"

	outFormatText = "text"
	outFormatJSON = "json"

	cleanupTimeout = 3 * time.Second
)

var (
	errNoAddr        = errors.New("target container must have at least one IP address")
	errBadLocalPort  = errors.New("bad local port")
	errBadRemoteHost = errors.New("bad remote host")
	errBadRemotePort = errors.New("bad remote port")
)

type options struct {
	target         string
	locals         []string
	remotes        []string
	runningTimeout time.Duration
	output         string
	quiet          bool
}

func NewCommand(cli cliutil.CLI) *cobra.Command {
	var opts options

	cmd := &cobra.Command{
		Use:   "port-forward CONTAINER -L [LOCAL:]REMOTE [-L ...] | -R [REMOTE:]:LOCAL [-R ...]",
		Short: `Forward one or more local or remote ports`,
		Long: `While the implementation for sure differs, the behavior and semantic of the command
are meant to be similar to SSH local (-L) and remote (-R) port forwarding. The word "local" always
refers to the cdebug side. The word "remote" always refers to the target container side.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(opts.locals)+len(opts.remotes) == 0 {
				return cliutil.NewStatusError(1, "at least one -L or -R flag must be provided")
			}
			if len(opts.remotes) > 0 {
				// TODO: Implement me!
				return cliutil.NewStatusError(1, "remote port forwarding is not implemented yet")
			}

			cli.SetQuiet(opts.quiet)

			opts.target = args[0]

			return cliutil.WrapStatusError(runPortForward(context.Background(), cli, &opts))
		},
	}

	flags := cmd.Flags()

	flags.StringSliceVarP(
		&opts.locals,
		"local",
		"L",
		nil,
		`Local port forwarding in the form [[LOCAL_HOST:]LOCAL_PORT:][REMOTE_HOST:]REMOTE_PORT`,
	)

	flags.StringSliceVarP(
		&opts.remotes,
		"remote",
		"R",
		nil,
		`Remote port forwarding in the form [REMOTE_HOST:]REMOTE_PORT:LOCAL_HOST:LOCAL_PORT`,
	)

	flags.DurationVar(
		&opts.runningTimeout,
		"running-timeout",
		10*time.Second,
		`How long to wait until the target is up and running`,
	)

	flags.BoolVarP(
		&opts.quiet,
		"quiet",
		"q",
		false,
		`Suppress verbose output`,
	)

	return cmd
}

func runPortForward(ctx context.Context, cli cliutil.CLI, opts *options) error {
	client, err := docker.NewClient(cli.AuxStream())
	if err != nil {
		return err
	}

	// TODO: Pull only if not present locally.
	cli.PrintAux("Pulling forwarder image...\n")
	if err := client.ImagePullEx(ctx, forwarderImage, types.ImagePullOptions{}); err != nil {
		return fmt.Errorf("cannot pull forwarder image %q: %w", forwarderImage, err)
	}

	ctx, cancel := context.WithCancel(signalutil.InterruptibleContext(ctx))
	defer cancel()

	for {
		cont, err := runLocalPortForwarding(ctx, cli, client, opts)
		if err != nil {
			return err
		}
		if !cont || ctx.Err() != nil {
			cli.PrintAux("Forwarding's done. Exiting...\n")
			return nil
		}

		cli.PrintAux("Giving target %s to get up and running again...\n", opts.runningTimeout)
	}
}

func runLocalPortForwarding(
	ctx context.Context,
	cli cliutil.CLI,
	client dockerclient.CommonAPIClient,
	opts *options,
) (bool, error) {
	target, err := getRunningTarget(ctx, client, opts.target, opts.runningTimeout)
	if err != nil {
		return false, err
	}

	if err := validateTarget(target); err != nil {
		return false, err
	}

	locals, err := parseLocalForwardings(target, opts.locals)
	if err != nil {
		return false, err
	}

	// Start a new context bound to a single target lifecycle.
	// It'll be used mostly to terminate the forwarders if a
	// given instance of the target terminates.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fwdersErrorCh := startLocalForwarders(ctx, cli, client, target, locals)

	targetStatusCh, targetErrorCh := client.ContainerWait(
		ctx,
		target.ID,
		container.WaitConditionNotRunning,
	)

	select {
	case err := <-fwdersErrorCh:
		// Couldn't start or keep one or more forwarders running.
		// All forwarders must be down (best effort) at this time.
		return false, err

	case <-targetStatusCh:
		// Target exited/restarting.
		cli.PrintAux("Target exited\n")

	case err := <-targetErrorCh:
		// No idea what happened to the target, but better restart the forwarders
		// (or exit while trying because the target is already gone).
		if ctx.Err() == nil { // Ignoring 'context canceled' errors...
			logrus.Debugf("Target error: %s", err)
		}
	}

	cli.PrintAux("Stopping the forwarders...\n")
	cancel() // Tell the forwarders it's time to stop.
	if err := <-fwdersErrorCh; err != nil {
		logrus.Debugf("Error stopping forwarder(s): %s", err)
	}

	if opts.runningTimeout == 0 {
		return false, nil
	}

	return true, nil
}

func getRunningTarget(
	ctx context.Context,
	client dockerclient.CommonAPIClient,
	target string,
	runningTimeout time.Duration,
) (types.ContainerJSON, error) {
	ctx, cancel := context.WithTimeout(ctx, runningTimeout)
	defer cancel()

	for {
		cont, err := client.ContainerInspect(ctx, target)
		if err != nil {
			return cont, err
		}
		if cont.State != nil && cont.State.Running {
			return cont, nil
		}

		select {
		case <-ctx.Done():
			return cont, fmt.Errorf("target is not running after %s", runningTimeout)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func validateTarget(target types.ContainerJSON) error {
	hasIP := false
	for _, net := range target.NetworkSettings.Networks {
		hasIP = hasIP || len(net.IPAddress) > 0
	}
	if !hasIP {
		return errNoAddr
	}

	return nil
}

type forwarding struct {
	localHost  string
	localPort  string
	remoteHost string
	remotePort string
}

type directForwarding struct {
	forwarding
	targetNetwork string
}

type sidecarForwarding struct {
	forwarding
	targetID      string // for netns
	targetNetwork string
	targetHost    string
	sidecarPort   string
}

func parseLocalForwardings(
	target types.ContainerJSON,
	locals []string,
) ([]forwarding, error) {
	var parsed []forwarding
	for _, l := range locals {
		next, err := parseLocalForwarding(target, l)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, next)
	}
	return parsed, nil
}

func parseLocalForwarding(
	target types.ContainerJSON,
	local string,
) (forwarding, error) {
	parts := strings.Split(local, ":")
	if len(parts) == 1 {
		// Case 1: REMOTE_PORT only
		if _, err := nat.ParsePort(parts[0]); err != nil {
			return forwarding{}, errBadRemotePort
		}

		if _, err := unambiguousIP(target); err != nil {
			return forwarding{}, err
		}

		return forwarding{
			remotePort: parts[0],
		}, nil
	}

	if len(parts) == 2 { // Either LOCAL_PORT:REMOTE_PORT or REMOTE_HOST:REMOTE_PORT
		if _, err := nat.ParsePort(parts[1]); err != nil {
			return forwarding{}, errBadRemotePort
		}

		if _, err := nat.ParsePort(parts[0]); err == nil {
			// Case 2: LOCAL_PORT:REMOTE_PORT
			if _, err := unambiguousIP(target); err != nil {
				return forwarding{}, err
			}

			return forwarding{
				localPort:  parts[0],
				remotePort: parts[1],
			}, nil
		}

		// Case 3: REMOTE_HOST:REMOTE_PORT
		return forwarding{
			remoteHost: parts[0],
			remotePort: parts[1],
		}, nil
	}

	if len(parts) == 3 {
		// Either LOCAL_PORT:REMOTE_HOST:REMOTE_PORT or (LOCAL_HOST:LOCAL_PORT:REMOTE_PORT | LOCAL_HOST::REMOTE_PORT)
		if _, err := nat.ParsePort(parts[2]); err != nil {
			return forwarding{}, errBadRemotePort
		}

		if _, err := nat.ParsePort(parts[0]); err == nil {
			// Case 4: LOCAL_PORT:REMOTE_HOST:REMOTE_PORT
			if len(parts[1]) == 0 {
				return forwarding{}, errBadRemoteHost
			}

			return forwarding{
				localPort:  parts[0],
				remoteHost: parts[1],
				remotePort: parts[2],
			}, nil
		}

		// Case 5: LOCAL_HOST:LOCAL_PORT:REMOTE_PORT or LOCAL_HOST::REMOTE_PORT
		if _, err := unambiguousIP(target); err != nil {
			return forwarding{}, err
		}

		return forwarding{
			localHost:  parts[0],
			localPort:  parts[1],
			remotePort: parts[2],
		}, nil
	}

	// Case 6: LOCAL_HOST:LOCAL_PORT:REMOTE_HOST:REMOTE_PORT or LOCAL_HOST::REMOTE_HOST:REMOTE_PORT
	if _, err := nat.ParsePort(parts[1]); err != nil && len(parts[1]) > 0 {
		return forwarding{}, errBadLocalPort
	}
	if _, err := nat.ParsePort(parts[3]); err != nil {
		return forwarding{}, errBadRemotePort
	}

	return forwarding{
		localHost:  parts[0],
		localPort:  parts[1],
		remoteHost: parts[2],
		remotePort: parts[3],
	}, nil
}

func unambiguousIP(target types.ContainerJSON) (string, error) {
	var found string
	for _, net := range target.NetworkSettings.Networks {
		if len(net.IPAddress) > 0 {
			if len(found) > 0 {
				return "", errors.New("remote IP must be specified explicitly for targets with multiple network interfaces")
			}
			found = net.IPAddress
		}
	}

	if len(found) == 0 {
		// This cannot really happen unless there is a mistake in validateTarget().
		return "", errNoAddr
	}

	return found, nil
}

func lookupTargetIP(target types.ContainerJSON, ipAliasNetwork string) (string, error) {
	for name, net := range target.NetworkSettings.Networks {
		if len(net.IPAddress) == 0 {
			continue
		}

		if net.IPAddress == ipAliasNetwork {
			return net.IPAddress, nil
		}

		for _, alias := range net.Aliases {
			if alias == ipAliasNetwork {
				return net.IPAddress, nil
			}
		}

		if name == ipAliasNetwork {
			return net.IPAddress, nil
		}
	}

	return "", errors.New("cannot derive remote host")
}

func lookupPortBindings(target types.ContainerJSON, targetPort string) []nat.PortBinding {
	for port, bindings := range target.NetworkSettings.Ports {
		if targetPort == port.Port() {
			return bindings
		}
	}
	return nil
}

func targetNetworkByIP(target types.ContainerJSON, ip string) (string, error) {
	for name, net := range target.NetworkSettings.Networks {
		if net.IPAddress == ip {
			return name, nil
		}
	}
	return "", errors.New("cannot deduce target network by IP")
}

func startLocalForwarders(
	ctx context.Context,
	cli cliutil.CLI,
	client dockerclient.CommonAPIClient,
	target types.ContainerJSON,
	locals []forwarding,
) <-chan error {
	doneCh := make(chan error, 1)

	go func() {
		var errored bool
		var wg sync.WaitGroup

		for _, fwd := range locals {
			wg.Add(1)

			go func(fwd forwarding) {
				defer wg.Done()

				if err := runLocalForwarder(ctx, cli, client, target, fwd); err != nil {
					logrus.Debugf("Forwarding error: %s", err)
					errored = true
				}
			}(fwd)
		}

		wg.Wait()
		if errored {
			doneCh <- errors.New("one or more forwarders failed")
		}
		close(doneCh)
	}()

	return doneCh
}

func runLocalForwarder(
	ctx context.Context,
	cli cliutil.CLI,
	client dockerclient.CommonAPIClient,
	target types.ContainerJSON,
	fwd forwarding,
) error {
	if len(fwd.localHost) == 0 {
		fwd.localHost = "127.0.0.1"
	}

	if len(fwd.remoteHost) == 0 {
		remoteIP, err := unambiguousIP(target)
		if err != nil {
			return err
		}

		network, err := targetNetworkByIP(target, remoteIP)
		if err != nil {
			return err
		}

		return runLocalDirectForwarder(
			ctx,
			cli,
			client,
			directForwarding{
				targetNetwork: network,
				forwarding: forwarding{
					localHost:  fwd.localHost,
					localPort:  fwd.localPort,
					remoteHost: remoteIP,
					remotePort: fwd.remotePort,
				},
			},
		)
	}

	if remoteIP, err := lookupTargetIP(target, fwd.remoteHost); err == nil {
		network, err := targetNetworkByIP(target, remoteIP)
		if err != nil {
			return err
		}

		return runLocalDirectForwarder(
			ctx,
			cli,
			client,
			directForwarding{
				targetNetwork: network,
				forwarding: forwarding{
					localHost:  fwd.localHost,
					localPort:  fwd.localPort,
					remoteHost: remoteIP,
					remotePort: fwd.remotePort,
				},
			},
		)
	}

	// In a multi-network case, pick a random one.
	var targetNetwork, targetIP string
	for name, settings := range target.NetworkSettings.Networks {
		if len(settings.IPAddress) > 0 {
			targetNetwork = name
			targetIP = settings.IPAddress
			break
		}
	}
	if len(targetNetwork) == 0 || len(targetIP) == 0 {
		return errors.New("target is not attached to any networks")
	}

	return runLocalSidecarForwarder(
		ctx,
		cli,
		client,
		sidecarForwarding{
			targetID:      target.ID,
			targetNetwork: targetNetwork,
			targetHost:    targetIP,
			forwarding:    fwd, // as is
		},
	)
}

func runLocalDirectForwarder(
	ctx context.Context,
	cli cliutil.CLI,
	client dockerclient.CommonAPIClient,
	fwd directForwarding,
) error {
	// TODO: Try start() N times.

	forwarderID, err := startLocalDirectForwarder(ctx, client, fwd)
	defer cleanupContainerIfExist(client, forwarderID)
	if err != nil {
		return fmt.Errorf("starting forwarder failed: %w", err)
	}

	if err := printLocalDirectForwarding(ctx, cli, client, fwd, forwarderID); err != nil {
		return err
	}

	fwderStatusCh, fwderErrCh := client.ContainerWait(
		ctx,
		forwarderID,
		container.WaitConditionNotRunning,
	)

	// TODO: If a forwarder was alive long enough, but then suddenly exited,
	//       we may want to restart it w/o decreasing the number of attempts.
	select {
	case <-ctx.Done():
		return nil

	case status := <-fwderStatusCh:
		return fmt.Errorf(
			"forwarder %s exited with code %d: %v",
			forwarderID, status.StatusCode, status.Error,
		)

	case err := <-fwderErrCh:
		logrus.Debugf("Forwarder error: %s", err)
		return fmt.Errorf("forwarder %s hiccuped: %w", forwarderID, err)
	}
}

func startLocalDirectForwarder(
	ctx context.Context,
	client dockerclient.CommonAPIClient,
	fwd directForwarding,
) (string, error) {
	portMapSpec := fwd.localHost + ":" + fwd.localPort + ":" + fwd.remotePort
	exposedPorts, portBindings, err := nat.ParsePortSpecs([]string{portMapSpec})
	if err != nil {
		return "", err
	}

	resp, err := client.ContainerCreate(
		ctx,
		&container.Config{
			Image:      forwarderImage,
			Entrypoint: []string{"socat"},
			Cmd: []string{
				fmt.Sprintf("TCP4-LISTEN:%s,fork", fwd.remotePort),
				fmt.Sprintf("TCP-CONNECT:%s:%s", fwd.remoteHost, fwd.remotePort),
			},
			Env:          []string{"SOCAT_DEFAULT_LISTEN_IP=0.0.0.0"},
			ExposedPorts: exposedPorts,
		},
		&container.HostConfig{
			PortBindings: portBindings,
			NetworkMode:  container.NetworkMode(fwd.targetNetwork),
		},
		nil,
		nil,
		"cdebug-fwd-"+uuid.ShortID(),
	)
	if err != nil {
		return "", fmt.Errorf("cannot create forwarder container: %w", err)
	}

	if err := client.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return resp.ID, fmt.Errorf("cannot start forwarder container: %w", err)
	}

	return resp.ID, nil
}

func runLocalSidecarForwarder(
	ctx context.Context,
	cli cliutil.CLI,
	client dockerclient.CommonAPIClient,
	fwd sidecarForwarding,
) error {
	// TODO: Try starting sidecar and forwarder N times.

	sidecarID, sidecarPort, err := startLocalSidecarForwarder(
		ctx, client, fwd.targetID, fwd.remoteHost, fwd.remotePort,
	)
	defer cleanupContainerIfExist(client, sidecarID)
	if err != nil {
		return fmt.Errorf("starting forwarder sidecar failed: %w", err)
	}

	fwd.sidecarPort = sidecarPort // randomly chosen

	forwarderID, err := startLocalDirectForwarder(
		ctx,
		client,
		directForwarding{
			targetNetwork: fwd.targetNetwork,
			forwarding: forwarding{
				localHost:  fwd.localHost,
				localPort:  fwd.localPort,
				remoteHost: fwd.targetHost,
				remotePort: fwd.sidecarPort,
			},
		},
	)
	defer cleanupContainerIfExist(client, forwarderID)
	if err != nil {
		return fmt.Errorf("starting forwarder faield: %w", err)
	}

	if err := printLocalSidecarForwarding(ctx, cli, client, fwd, forwarderID); err != nil {
		return err
	}

	sidecarStatusCh, sidecarErrCh := client.ContainerWait(
		ctx,
		sidecarID,
		container.WaitConditionNotRunning,
	)

	fwderStatusCh, fwderErrCh := client.ContainerWait(
		ctx,
		forwarderID,
		container.WaitConditionNotRunning,
	)

	// TODO: If a forwarder and/or was alive long enough, we may want to
	//       restart them w/o decreasing the number of attempts.
	select {
	case <-ctx.Done():
		return nil

	case status := <-sidecarStatusCh:
		return fmt.Errorf(
			"forwarder sidecar %s exited with code %d: %v",
			sidecarID, status.StatusCode, status.Error,
		)

	case status := <-fwderStatusCh:
		return fmt.Errorf(
			"forwarder %s exited with code %d: %v",
			forwarderID, status.StatusCode, status.Error,
		)

	case err := <-sidecarErrCh:
		logrus.Debugf("Forwarder sidecar error: %s", err)
		return fmt.Errorf("forwarder sidecar %s hiccuped: %w", sidecarID, err)

	case err := <-fwderErrCh:
		logrus.Debugf("Forwarder error: %s", err)
		return fmt.Errorf("forwarder %s hiccuped: %w", forwarderID, err)
	}
}

func startLocalSidecarForwarder(
	ctx context.Context,
	client dockerclient.CommonAPIClient,
	targetID string,
	remoteHost string,
	remotePort string,
) (string, string, error) {
	// TODO: This random port may conflict with a port already used by the
	//       target container. Instead, we should use socat TCP-LISTEN:0 and
	//       detect what port was assigned by the OS with a separate command.
	randomPort := fmt.Sprintf("%d", 32000+rand.Intn(25000))
	resp, err := client.ContainerCreate(
		ctx,
		&container.Config{
			Image:      forwarderImage,
			Entrypoint: []string{"socat"},
			Cmd: []string{
				fmt.Sprintf("TCP4-LISTEN:%s,fork", randomPort),
				fmt.Sprintf("TCP-CONNECT:%s:%s", remoteHost, remotePort),
			},
			Env: []string{"SOCAT_DEFAULT_LISTEN_IP=0.0.0.0"},
		},
		&container.HostConfig{
			NetworkMode: container.NetworkMode("container:" + targetID),
		},
		nil,
		nil,
		"cdebug-fwd-sidecar-"+uuid.ShortID(),
	)
	if err != nil {
		return "", "", fmt.Errorf("cannot create forwarder sidecar container: %w", err)
	}

	if err := client.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return resp.ID, "", fmt.Errorf("cannot start forwarder sidecar container: %w", err)
	}

	return resp.ID, randomPort, nil
}

func printLocalDirectForwarding(
	ctx context.Context,
	cli cliutil.CLI,
	client dockerclient.CommonAPIClient,
	fwd directForwarding,
	forwarderID string,
) error {
	if len(fwd.localPort) == 0 {
		forwarder, err := client.ContainerInspect(ctx, forwarderID)
		if err != nil {
			return fmt.Errorf("cannot inspect forwarder container: %w", err)
		}

		bindings := lookupPortBindings(forwarder, fwd.remotePort)
		if len(bindings) == 0 {
			logrus.Debugf("Empty port bindings in forwarder %s", forwarder.ID)
			fwd.localPort = "<unknown>"
		} else {
			// Every forwarder should have just one port exposed.
			fwd.localPort = bindings[0].HostPort
		}
	}

	cli.PrintOut(
		"Forwarding %s:%s to %s:%s\n",
		fwd.localHost, fwd.localPort,
		fwd.remoteHost, fwd.remotePort,
	)

	return nil
}

func printLocalSidecarForwarding(
	ctx context.Context,
	cli cliutil.CLI,
	client dockerclient.CommonAPIClient,
	fwd sidecarForwarding,
	forwarderID string,
) error {
	if len(fwd.localPort) == 0 {
		forwarder, err := client.ContainerInspect(ctx, forwarderID)
		if err != nil {
			return fmt.Errorf("cannot inspect forwarder container: %w", err)
		}

		bindings := lookupPortBindings(forwarder, fwd.sidecarPort)
		if len(bindings) == 0 {
			logrus.Debugf("Empty port bindings in forwarder %s", forwarder.ID)
			fwd.localPort = "<unknown>"
		} else {
			// Every forwarder should have just one port exposed.
			fwd.localPort = bindings[0].HostPort
		}
	}

	cli.PrintOut(
		"Forwarding %s:%s to %s:%s through %s:%s\n",
		fwd.localHost, fwd.localPort,
		fwd.remoteHost, fwd.remotePort,
		fwd.targetHost, fwd.sidecarPort,
	)

	return nil
}

func cleanupContainerIfExist(
	client dockerclient.CommonAPIClient,
	contID string,
) {
	if len(contID) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	if err := client.ContainerRemove(ctx, contID, types.ContainerRemoveOptions{Force: true}); err != nil {
		logrus.Debugf("Cannot force-remove container %s: %s", contID, err)
	}
}
