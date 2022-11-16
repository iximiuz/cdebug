package portforward

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/iximiuz/cdebug/pkg/jsonutil"
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
//   - REMOTE_PORT                                # binds REMOTE_IP:REMOTE_PORT to a random port on localhost
//   - REMOTE_<IP|ALIAS|NET>:REMOTE_PORT          # The second form is needed to:
//                                                #  1) allow exposing target's localhost ports
//                                                #  2) specify a concrete IP for a multi-network target
//
//   - LOCAL_PORT:REMOTE_PORT                     # binds REMOTE_IP:REMOTE_PORT to LOCAL_PORT on localhost
//   - LOCAL_PORT:REMOTE_<IP|ALIAS|NET>:REMOTE_PORT
//
//   - LOCAL_HOST:LOCAL_PORT:REMOTE_PORT          # similar to LOCAL_PORT:REMOTE_PORT but LOCAL_HOST is used instead of localhost
//   - LOCAL_HOST:LOCAL_PORT:REMOTE_<IP|ALIAS|NET>:REMOTE_PORT
//
// Remote port forwarding's possible modes (kinda sorta as in ssh -R):
//   - coming soon...

const (
	forwarderImage = "nixery.dev/shell/socat:latest"

	outFormatText = "text"
	outFormatJSON = "json"
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

	cli.PrintAux("Pulling forwarder image...\n")
	if err := client.ImagePullEx(ctx, forwarderImage, types.ImagePullOptions{}); err != nil {
		return fmt.Errorf("cannot pull forwarder image %q: %w", forwarderImage, err)
	}

	ctx, cancel := context.WithCancel(signalutil.InterruptibleContext(ctx))
	defer cancel()

	for {
		cont, err := runLocalPortForward(ctx, cli, client, opts)
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

func runLocalPortForward(
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
		logrus.Debugf("Target error: %s", err)
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

			go func() {
				defer wg.Done()

				if err := runLocalForwarder(ctx, cli, client, target, fwd); err != nil {
					logrus.Debugf("Forwarding error: %s", err)
					errored = true
				}
			}()
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
	if len(fwd.remoteHost) == 0 {
		remoteIP, err := unambiguousIP(target)
		if err != nil {
			return err
		}
		return runLocalForwarderToTargetPublicPort(
			ctx,
			cli,
			client,
			target,
			fwd.localHost,
			fwd.localPort,
			remoteIP,
			fwd.remotePort,
		)
	}

	if remoteIP, err := lookupTargetIP(target, fwd.remoteHost); err == nil {
		return runLocalForwarderToTargetPublicPort(
			ctx,
			cli,
			client,
			target,
			fwd.localHost,
			fwd.localPort,
			remoteIP,
			fwd.remotePort,
		)
	}

	// TODO: runLocalForwarderThroughTargetNetns()
	return errors.New("implement me!")
}

func runLocalForwarderToTargetPublicPort(
	ctx context.Context,
	cli cliutil.CLI,
	client dockerclient.CommonAPIClient,
	target types.ContainerJSON,
	localHost string,
	localPort string,
	remoteIP string,
	remotePort string,
) error {
	// TODO: Try start() N times.
	forwarder, err := startLocalForwarderToTargetPublicPort(
		ctx,
		cli,
		client,
		target,
		localHost,
		localPort,
		remoteIP,
		remotePort,
	)
	if err != nil {
		return err
	}

	killForwarder := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		if err := client.ContainerKill(ctx, forwarder.ID, "KILL"); err != nil {
			logrus.Debugf("Cannot kill forwarder container: %s", err)
		}
	}

	fwderStatusCh, fwderErrCh := client.ContainerWait(
		ctx,
		forwarder.ID,
		container.WaitConditionNotRunning,
	)

	// TODO: If a forwarder was alive long enough, we may want to restart it w/o
	//       decreasing the number of attempts.
	select {
	case <-ctx.Done():
		killForwarder()
		return nil

	case status := <-fwderStatusCh:
		return fmt.Errorf(
			"forwarder %s exited with code %d: %w",
			forwarder.ID, status.StatusCode, status.Error,
		)

	case err := <-fwderErrCh:
		logrus.Debugf("Forwarder error: %s", err)
		killForwarder() // Just in case...
		return fmt.Errorf("forwarder %s hiccuped: %w", forwarder.ID, err)
	}
}

func startLocalForwarderToTargetPublicPort(
	ctx context.Context,
	cli cliutil.CLI,
	client dockerclient.CommonAPIClient,
	target types.ContainerJSON,
	localHost string,
	localPort string,
	remoteIP string,
	remotePort string,
) (types.ContainerJSON, error) {
	if len(localHost) == 0 {
		localHost = "127.0.0.1"
	}

	network, err := targetNetworkByIP(target, remoteIP)
	if err != nil {
		return types.ContainerJSON{}, err
	}

	portMapSpec := localHost + ":" + localPort + ":" + remotePort
	exposedPorts, portBindings, err := nat.ParsePortSpecs([]string{portMapSpec})
	if err != nil {
		return types.ContainerJSON{}, err
	}

	resp, err := client.ContainerCreate(
		ctx,
		&container.Config{
			Image:      forwarderImage,
			Entrypoint: []string{"socat"},
			Cmd: []string{
				fmt.Sprintf("TCP-LISTEN:%s,fork", remotePort),
				fmt.Sprintf("TCP-CONNECT:%s:%s", remoteIP, remotePort),
			},
			ExposedPorts: exposedPorts,
		},
		&container.HostConfig{
			AutoRemove:   true,
			PortBindings: portBindings,
			NetworkMode:  container.NetworkMode(network),
		},
		nil,
		nil,
		"cdebug-fwd-"+uuid.ShortID(),
	)
	if err != nil {
		return types.ContainerJSON{}, fmt.Errorf("cannot create forwarder container: %w", err)
	}

	if err := client.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return types.ContainerJSON{}, fmt.Errorf("cannot start forwarder container: %w", err)
	}

	forwarder, err := client.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return forwarder, fmt.Errorf("cannot inspect forwarder container: %w", err)
	}
	if forwarder.State == nil && !forwarder.State.Running {
		return forwarder, fmt.Errorf("unexpected forwarder container state: %v", jsonutil.Dump(forwarder.State))
	}

	if len(localPort) == 0 {
		bindings := lookupPortBindings(forwarder, remotePort)
		if len(bindings) == 0 {
			logrus.Debugf("Empty bindings for port map spec %s in forwarder %s", portMapSpec, forwarder.ID)
			localPort = "<unknown>"
		} else {
			// Every forwarder should have just one port exposed.
			localPort = bindings[0].HostPort
		}
	}

	cli.PrintOut("Forwarding %s:%s to %s:%s\n", localHost, localPort, remoteIP, remotePort)
	return forwarder, nil
}
