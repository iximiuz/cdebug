package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Masterminds/semver"
	"github.com/containerd/console"
	offcontainerd "github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/cmd/ctr/commands/tasks"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/sirupsen/logrus"
	"golang.org/x/term"

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

	ociSpecNetNS, err := ociSpecContainerNetNS(ctx, target)
	if err != nil {
		return err
	}

	debugger, err := client.NewContainer(
		ctx,
		runName,
		offcontainerd.WithNewSnapshot(runName, image),
		offcontainerd.WithNewSpec(oci.Compose(
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
			ociSpecNetNS,
		)),

		// NetworkMode
		// PidMode
		// UTSMode
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

	var con console.Console
	if flagT {
		con = console.Current()
		defer con.Reset()
		if err := con.SetRaw(); err != nil {
			return err
		}
	}

	// OpenStdin: opts.stdin
	// AttachStdin:  opts.stdin,
	// AttachStdout: true,
	// AttachStderr: true,
	task, err := debugger.NewTask(ctx, nil)
	if err != nil {
		return err
	}

	// TODO: Attach to the debugger task
	// TODO: Screen resizing

	waitCh, err := task.Wait(ctx)
	if err != nil {
		return err
	}

	if err := task.Start(ctx); err != nil {
		return err
	}

	if err := tasks.HandleConsoleResize(ctx, task, con); err != nil {
		logrus.WithError(err).Error("console resize")
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

func ociSpecNoOp(context.Context, oci.Client, *containers.Container, *oci.Spec) error {
	return nil
}

func NewTask(
	ctx context.Context,
	client *containerd.Client,
	container containerd.Container,
	flagI, flagT bool,
	con console.Console,
) (containerd.Task, error) {
	var ioCreator cio.Creator
	if flagT {
		if con == nil {
			return nil, errors.New("got nil con with flagT=true")
		}
		var in io.Reader
		if flagI {
			// FIXME: check IsTerminal on Windows too
			if runtime.GOOS != "windows" && !term.IsTerminal(0) {
				return nil, errors.New("the input device is not a TTY")
			}
			in = con
		}
		ioCreator = cio.NewCreator(cio.WithStreams(in, con, nil), cio.WithTerminal)
	} else {
		var in io.Reader
		if flagI {
			if sv, err := infoutil.ServerSemVer(ctx, client); err != nil {
				logrus.Warn(err)
			} else if sv.LessThan(semver.MustParse("1.6.0-0")) {
				logrus.Warnf("`nerdctl (run|exec) -i` without `-t` expects containerd 1.6 or later, got containerd %v", sv)
			}
			var stdinC io.ReadCloser = &StdinCloser{
				Stdin: os.Stdin,
				Closer: func() {
					if t, err := container.Task(ctx, nil); err != nil {
						logrus.WithError(err).Debugf("failed to get task for StdinCloser")
					} else {
						t.CloseIO(ctx, containerd.WithStdinCloser)
					}
				},
			}
			in = stdinC
		}
		ioCreator = cio.NewCreator(cio.WithStreams(in, os.Stdout, os.Stderr))
	}

	return container.NewTask(ctx, ioCreator)
}

// StdinCloser is from https://github.com/containerd/containerd/blob/v1.4.3/cmd/ctr/commands/tasks/exec.go#L181-L194
type StdinCloser struct {
	mu     sync.Mutex
	Stdin  *os.File
	Closer func()
	closed bool
}

func (s *StdinCloser) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, syscall.EBADF
	}
	n, err := s.Stdin.Read(p)
	if err != nil {
		if s.Closer != nil {
			s.Closer()
			s.closed = true
		}
	}
	return n, err
}

// Close implements Closer
func (s *StdinCloser) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if s.Closer != nil {
		s.Closer()
	}
	s.closed = true
	return nil
}
