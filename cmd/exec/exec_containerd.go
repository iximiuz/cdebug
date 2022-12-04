package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/console"
	offcontainerd "github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/cmd/ctr/commands/tasks"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/sirupsen/logrus"

	"github.com/iximiuz/cdebug/pkg/cliutil"
	"github.com/iximiuz/cdebug/pkg/containerd"
	"github.com/iximiuz/cdebug/pkg/uuid"
)

func runDebuggerContainerd(ctx context.Context, cli cliutil.CLI, opts *options) error {
	if strings.ContainsAny(opts.namespace, "/.") {
		return errors.New("namespaces with '/' or '.' are unsupported")
	}

	client, err := containerd.NewClient(containerd.Options{
		Out:       cli.AuxStream(),
		Address:   opts.runtime,
		Namespace: opts.namespace,
	})
	if err != nil {
		return err
	}

	ctx = namespaces.WithNamespace(ctx, client.Namespace())

	found, err := client.Containers(ctx, fmt.Sprintf("id~=^%s.*$", regexp.QuoteMeta(opts.target)))
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return errTargetNotFound
	}
	if len(found) > 1 {
		return errors.New("ambiguous target partial ID")
	}
	target := found[0]

	if task, err := target.Task(ctx, nil); err != nil {
		return err
	} else {
		if status, err := task.Status(ctx); err != nil {
			return err
		} else if status.Status != offcontainerd.Running {
			return errTargetNotRunning
		}
	}

	targetSpec, err := target.Spec(ctx)
	if err != nil {
		return err
	}

	cli.PrintAux("Pulling debugger image...\n")
	image, err := client.ImagePullEx(ctx, opts.image)
	if err != nil {
		return errCannotPull(opts.image, err)
	}

	runID := uuid.ShortID()
	runName := debuggerName(opts.name, runID)

	ociSpecNamespaces, err := ociSpecSharedNamespaces(ctx, target)
	if err != nil {
		return err
	}

	debugger, err := client.NewContainer(
		ctx,
		runName,
		offcontainerd.WithNewSnapshot(runName, image),
		offcontainerd.WithNewSpec(
			oci.Compose(
				append(
					[]oci.SpecOpts{
						oci.WithImageConfig(image),
						oci.WithProcessArgs("sh", "-c", debuggerEntrypoint(cli, runID, opts.image, opts.cmd)),
						func() oci.SpecOpts {
							if opts.tty {
								return oci.WithTTY
							}
							return ociSpecNoOp
						}(),
						func() oci.SpecOpts {
							if opts.privileged {
								return oci.WithPrivileged
							}
							return ociSpecNoOp
						}(),
						oci.WithAddedCapabilities(targetSpec.Process.Capabilities.Bounding),
					},
					ociSpecNamespaces...,
				)...,
			),
		),
	)
	if err != nil {
		return errCannotCreate(err)
	}

	if opts.autoRemove {
		defer func() {
			ctx, cancel := context.WithTimeout(
				namespaces.WithNamespace(context.Background(), client.Namespace()),
				3*time.Second,
			)
			defer cancel()

			if err := client.ContainerRemoveEx(ctx, debugger, true); err != nil {
				logrus.Debugf("Cannot remove debugger container: %s", err)
			}
		}()
	}

	ioc, con, err := prepareTaskIO(ctx, cli, opts.tty, opts.stdin, debugger)
	if err != nil {
		return err
	}
	if con != nil {
		defer con.Reset()
	}

	task, err := debugger.NewTask(ctx, ioc)
	if err != nil {
		return err
	}

	waitCh, err := task.Wait(ctx)
	if err != nil {
		return err
	}

	if err := task.Start(ctx); err != nil {
		return err
	}

	if opts.tty && cli.OutputStream().IsTerminal() {
		if err := tasks.HandleConsoleResize(ctx, task, con); err != nil {
			logrus.WithError(err).Error("console resize")
		}
	}

	status := <-waitCh
	if status.Error() != nil {
		return fmt.Errorf("waiting debugger container failed: %w", err)
	}
	return nil
}

func ociSpecContainerNetNS(
	ctx context.Context,
	cont offcontainerd.Container,
) (oci.SpecOpts, error) {
	// filepath.Join(dataStore, "containers", ns, id), nil
	// contStateDir, err := getContainerStateDirPath(cmd, dataStore, cont.ID())
	// if err != nil {
	// 	return nil, err
	// }

	spec, err := cont.Spec(ctx)
	if err != nil {
		return nil, err
	}

	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			return oci.WithLinuxNamespace(ns), nil
		}
	}

	return ociSpecNoOp, nil

	// fmt.Sprintf("/proc/%d/ns/net", task.Pid())
	// netNSPath, err := getContainerNetNSPath(ctx, cont)
	// if err != nil {
	// 	return nil, err
	// }

	// return oci.Compose(
	// 	oci.WithLinuxNamespace(specs.LinuxNamespace{
	// 		Type: specs.NetworkNamespace,
	// 		Path: netNSPath,
	// 	}),
	// 	withCustomResolvConf(filepath.Join(contStateDir, "resolv.conf")),
	// 	withCustomHosts(hostsstore.HostsPath(dataStore, ns, cont.ID())),
	// 	oci.WithHostname(spec.Hostname),
	// 	withCustomEtcHostname(filepath.Join(contStateDir, "hostname")),
	// ), nil
}

// func getContainerNetNSPath(ctx context.Context, c containerd.Container) (string, error) {
// 	task, err := c.Task(ctx, nil)
// 	if err != nil {
// 		return "", err
// 	}
// 	status, err := task.Status(ctx)
// 	if err != nil {
// 		return "", err
// 	}
// 	if status.Status != containerd.Running {
// 		return "", fmt.Errorf("invalid target container: %s, should be running", c.ID())
// 	}
// 	return fmt.Sprintf("/proc/%d/ns/net", task.Pid()), nil
// }

func ociSpecSharedNamespaces(
	ctx context.Context,
	cont offcontainerd.Container,
) ([]oci.SpecOpts, error) {
	// netNS, err := ociSpecContainerNetNS(ctx, cont)
	// if err != nil {
	// 	return nil, err
	// }

	shared := map[specs.LinuxNamespaceType]oci.SpecOpts{
		specs.NetworkNamespace: oci.WithHostNamespace(specs.NetworkNamespace),
		specs.PIDNamespace:     oci.WithHostNamespace(specs.PIDNamespace),
		specs.IPCNamespace:     oci.WithHostNamespace(specs.IPCNamespace),
		specs.UTSNamespace:     oci.WithHostNamespace(specs.UTSNamespace),
	}

	task, err := cont.Task(ctx, nil)
	if err != nil {
		return nil, err
	}

	spec, err := cont.Spec(ctx)
	if err != nil {
		return nil, err
	}

	fsType := map[specs.LinuxNamespaceType]string{
		specs.NetworkNamespace: "net",
		specs.PIDNamespace:     "pid",
		specs.IPCNamespace:     "ipc",
		specs.UTSNamespace:     "uts",
	}

	for _, ns := range spec.Linux.Namespaces {
		if _, ok := shared[ns.Type]; ok {
			shared[ns.Type] = oci.WithLinuxNamespace(specs.LinuxNamespace{
				Type: ns.Type,
				Path: fmt.Sprintf("/proc/%d/ns/%s", task.Pid(), fsType[ns.Type]),
			})
		}
	}

	list := []oci.SpecOpts{}
	for _, opt := range shared {
		list = append(list, opt)
	}
	return list, nil
}

func ociSpecNoOp(context.Context, oci.Client, *containers.Container, *oci.Spec) error {
	return nil
}

func prepareTaskIO(
	ctx context.Context,
	cli cliutil.CLI,
	tty bool,
	stdin bool,
	cont offcontainerd.Container,
) (cio.Creator, console.Console, error) {
	if tty {
		var con console.Console
		if cli.OutputStream().IsTerminal() {
			con = console.Current()
			if err := con.SetRaw(); err != nil {
				return nil, nil, err
			}
		}

		var in io.Reader
		if stdin {
			if con == nil {
				return nil, nil, errors.New("input must be a terminal")
			}
			in = con
		}

		return cio.NewCreator(cio.WithStreams(in, con, nil), cio.WithTerminal), con, nil
	}

	var in io.Reader
	if stdin {
		in = &inCloser{
			inputStream: cli.InputStream(),
			close: func() {
				if task, err := cont.Task(ctx, nil); err != nil {
					logrus.Debugf("Failed to get task for stdinCloser: %s", err)
				} else {
					task.CloseIO(ctx, offcontainerd.WithStdinCloser)
				}
			},
		}
	}

	return cio.NewCreator(cio.WithStreams(
		in,
		cli.OutputStream(),
		cli.ErrorStream(),
	)), nil, nil
}

type inCloser struct {
	inputStream io.Reader
	close       func()

	mu     sync.Mutex
	closed bool
}

func (s *inCloser) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, syscall.EBADF
	}

	n, err := s.inputStream.Read(p)
	if err != nil {
		if s.close != nil {
			s.close()
			s.closed = true
		}
	}

	return n, err
}

func (s *inCloser) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	if s.close != nil {
		s.close()
	}
	s.closed = true
	return nil
}
