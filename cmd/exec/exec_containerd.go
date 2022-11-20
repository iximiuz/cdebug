package exec

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	offcontainerd "github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"

	"github.com/iximiuz/cdebug/pkg/cliutil"
	"github.com/iximiuz/cdebug/pkg/containerd"
	"github.com/iximiuz/cdebug/pkg/uuid"
)

func runDebuggerContainerd(ctx context.Context, cli cliutil.CLI, opts *options) error {
	client, err := containerd.NewClient(containerd.Options{
		Out:       cli.AuxStream(),
		Address:   opts.runtime,
		Namespace: opts.namespace,
	})
	if err != nil {
		return err
	}

	ctx = namespaces.WithNamespace(ctx, client.Namespace())

	containers, err := client.Containers(ctx, fmt.Sprintf("id~=^%s.*$", regexp.QuoteMeta(opts.target)))
	if err != nil {
		return err
	}
	if len(containers) == 0 {
		return errTargetNotFound
	}
	if len(containers) > 1 {
		return errors.New("ambiguous target partial ID")
	}
	target := containers[0]

	task, err := target.Task(ctx, nil)
	if err != nil {
		return err
	}

	status, err := task.Status(ctx)
	if err != nil {
		return err
	}
	if status.Status != offcontainerd.Running {
		return errTargetNotRunning
	}

	cli.PrintAux("Pulling debugger image...\n")
	image, err := client.ImagePullEx(ctx, opts.image)
	if err != nil {
		return errCannotPull(opts.image, err)
	}

	runID := uuid.ShortID()
	runName := debuggerName(opts.name, runID)
	// See cmd/nerdctl/run_network.go
	debugger, err := client.NewContainer(
		ctx,
		runName,
		offcontainerd.WithNewSnapshot(runName, image),
		offcontainerd.WithNewSpec(oci.WithImageConfig(image)),
	)
	if err != nil {
		return errCannotCreate(err)
	}

	if _, err := debugger.NewTask(ctx, nil); err != nil {
		return err
	}

	return nil
}
