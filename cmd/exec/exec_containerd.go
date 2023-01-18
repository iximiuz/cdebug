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
	"github.com/containerd/containerd/cmd/ctr/commands"
	"github.com/containerd/containerd/cmd/ctr/commands/tasks"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/platforms"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/sirupsen/logrus"

	"github.com/iximiuz/cdebug/pkg/cliutil"
	"github.com/iximiuz/cdebug/pkg/containerd"
	"github.com/iximiuz/cdebug/pkg/uuid"
)

func runDebuggerContainerd(ctx context.Context, cli cliutil.CLI, opts *options) error {
	if strings.Contains(opts.namespace, "/") {
		return errors.New("namespaces with '/' are unsupported")
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

	filters := []string{
		fmt.Sprintf("id~=^%s.*$", regexp.QuoteMeta(opts.target)),
	}
	if opts.schema == schemaNerdctl {
		// Tiny helper for nerdctl-started containers
		filters = append(filters, fmt.Sprintf(`labels."nerdctl/name"==%s`, opts.target))
	}

	found, err := client.Containers(ctx, filters...)
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

	targetTask, err := target.Task(ctx, nil)
	if err != nil {
		return err
	}
	if status, err := targetTask.Status(ctx); err != nil {
		return err
	} else if status.Status != offcontainerd.Running {
		return errTargetNotRunning
	}

	targetSpec, err := target.Spec(ctx)
	if err != nil {
		return err
	}

	cli.PrintAux("Pulling debugger image...\n")
	image, err := client.ImagePullEx(
		ctx,
		opts.image,
		func() string {
			if len(opts.platform) == 0 {
				return platforms.Format(platforms.DefaultSpec())
			}
			return opts.platform
		}(),
	)
	if err != nil {
		return errCannotPull(opts.image, err)
	}

	runID := uuid.ShortID()
	runName := debuggerName(opts.name, runID)

	targetPID := int(targetTask.Pid())
	if hasNamespace(targetSpec.Linux.Namespaces, specs.PIDNamespace) {
		targetPID = 1
	}

	debugger, err := client.NewContainer(
		ctx,
		runName,
		offcontainerd.WithNewSnapshot(runName, image),
		offcontainerd.WithNewSpec(
			oci.Compose(
				// Order is important here!
				oci.WithDefaultPathEnv,
				oci.WithImageConfig(image), // May override the default $PATH.
				oci.WithProcessArgs("sh", "-c", debuggerEntrypoint(
					cli, runID, targetPID, opts.image, opts.cmd,
				)),
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

					// Take the target's config as is:
					return oci.Compose(
						oci.WithCapabilities(targetSpec.Process.Capabilities.Effective),
						oci.WithMaskedPaths(targetSpec.Linux.MaskedPaths),
						oci.WithReadonlyPaths(targetSpec.Linux.ReadonlyPaths),
						// TODO: oci.WithWriteableSysfs,
						// TODO: oci.WithWriteableCgroupfs,
						oci.WithSelinuxLabel(targetSpec.Process.SelinuxLabel),
						oci.WithApparmorProfile(targetSpec.Process.ApparmorProfile),
						func(_ context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
							if s.Linux == nil {
								s.Linux = &specs.Linux{}
							}
							s.Linux.Seccomp = targetSpec.Linux.Seccomp
							return nil
						},
					)
				}(),
				debuggerNamespacesSpec(targetTask.Pid(), targetSpec.Linux.Namespaces),
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
	} else {
		sigc := commands.ForwardAllSignals(ctx, task)
		defer commands.StopCatch(sigc)
	}

	status := <-waitCh
	if status.Error() != nil {
		return fmt.Errorf("waiting debugger container failed: %w", err)
	}
	return nil
}

var (
	namespaceTypeMap = map[specs.LinuxNamespaceType]string{
		specs.NetworkNamespace: "net",
		specs.PIDNamespace:     "pid",
		specs.IPCNamespace:     "ipc",
		specs.UTSNamespace:     "uts",
	}
)

func debuggerNamespacesSpec(
	targetPID uint32,
	targetNamespaces []specs.LinuxNamespace,
) oci.SpecOpts {
	debuggerNamespaces := map[specs.LinuxNamespaceType]oci.SpecOpts{
		specs.NetworkNamespace: oci.WithHostNamespace(specs.NetworkNamespace),
		specs.PIDNamespace:     oci.WithHostNamespace(specs.PIDNamespace),
		specs.IPCNamespace:     oci.WithHostNamespace(specs.IPCNamespace),
		specs.UTSNamespace:     oci.WithHostNamespace(specs.UTSNamespace),
	}

	for _, ns := range targetNamespaces {
		if _, ok := debuggerNamespaces[ns.Type]; ok {
			debuggerNamespaces[ns.Type] = oci.WithLinuxNamespace(specs.LinuxNamespace{
				Type: ns.Type,
				Path: fmt.Sprintf("/proc/%d/ns/%s", targetPID, namespaceTypeMap[ns.Type]),
			})
		}
	}

	opts := []oci.SpecOpts{}
	for _, opt := range debuggerNamespaces {
		opts = append(opts, opt)
	}
	return oci.Compose(opts...)
}

func hasNamespace(list []specs.LinuxNamespace, typ specs.LinuxNamespaceType) bool {
	for _, ns := range list {
		if ns.Type == typ {
			return true
		}
	}
	return false
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
