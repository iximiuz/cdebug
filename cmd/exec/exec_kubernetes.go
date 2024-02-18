package exec

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/remotecommand"
	watchtools "k8s.io/client-go/tools/watch"

	"github.com/iximiuz/cdebug/pkg/cliutil"
	ckubernetes "github.com/iximiuz/cdebug/pkg/kubernetes"
	"github.com/iximiuz/cdebug/pkg/tty"
	"github.com/iximiuz/cdebug/pkg/uuid"
)

func runDebuggerKubernetes(ctx context.Context, cli cliutil.CLI, opts *options) error {
	if opts.autoRemove {
		return fmt.Errorf("--rm flag is not supported for Kubernetes")
	}

	config, namespace, err := ckubernetes.GetRESTConfig(
		opts.runtime,
		opts.kubeconfig,
		opts.kubeconfigContext,
	)
	if err != nil {
		return fmt.Errorf("error getting Kubernetes REST config: %v", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("error creating Kubernetes client: %v", err)
	}

	if opts.namespace != "" {
		namespace = opts.namespace
	}
	if namespace == "" {
		namespace = "default"
	}

	var (
		podName    string
		targetName string
	)
	opts.target = strings.TrimPrefix(opts.target, "pod/")
	opts.target = strings.TrimPrefix(opts.target, "pods/")
	if strings.Contains(opts.target, "/") {
		podName = strings.Split(opts.target, "/")[0]
		targetName = strings.Split(opts.target, "/")[1]
	} else {
		podName = opts.target
	}

	pod, err := client.
		CoreV1().
		Pods(namespace).
		Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting target pod: %v", err)
	}

	runID := uuid.ShortID()
	debuggerName := debuggerName(opts.name, runID)
	cli.PrintAux("Debugger container name: %s\n", debuggerName)

	cli.PrintAux("Starting debugger container...\n")
	if err := runPodDebugger(
		ctx,
		opts,
		client,
		pod,
		targetName,
		debuggerName,
		debuggerEntrypoint(cli, runID, 1, opts.image, opts.cmd, opts.user),
	); err != nil {
		return fmt.Errorf("error adding debugger container: %v", err)
	}

	return attachPodDebugger(
		ctx,
		cli,
		opts,
		config,
		client,
		namespace,
		podName,
		debuggerName,
	)
}

func runPodDebugger(
	ctx context.Context,
	opts *options,
	client kubernetes.Interface,
	pod *corev1.Pod,
	targetName string,
	debuggerName string,
	entrypoint string,
) error {
	podJSON, err := json.Marshal(pod)
	if err != nil {
		return fmt.Errorf("error creating JSON for pod: %v", err)
	}

	debugPod := withDebugContainer(pod, opts, targetName, debuggerName, entrypoint)
	if err != nil {
		return err
	}

	debugJSON, err := json.Marshal(debugPod)
	if err != nil {
		return fmt.Errorf("error creating JSON for debug container: %v", err)
	}

	patch, err := strategicpatch.CreateTwoWayMergePatch(podJSON, debugJSON, pod)
	if err != nil {
		return fmt.Errorf("error creating patch to add debug container: %v", err)
	}

	_, err = client.
		CoreV1().
		Pods(pod.Namespace).
		Patch(
			ctx,
			pod.Name,
			types.StrategicMergePatchType,
			patch,
			metav1.PatchOptions{},
			"ephemeralcontainers",
		)
	if err != nil {
		// The apiserver will return a 404 when the EphemeralContainers feature is disabled because the `/ephemeralcontainers` subresource
		// is missing. Unlike the 404 returned by a missing pod, the status details will be empty.
		if serr, ok := err.(*apierrors.StatusError); ok && serr.Status().Reason == metav1.StatusReasonNotFound && serr.ErrStatus.Details.Name == "" {
			return fmt.Errorf("ephemeral containers are disabled for this cluster (error from server: %q)", err)
		}

		return err
	}

	return nil
}

func withDebugContainer(
	pod *corev1.Pod,
	opts *options,
	targetName string,
	debuggerName string,
	entrypoint string,
) *corev1.Pod {
	ec := &corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name: debuggerName,
			// Env:                   o.Env,
			Image:                    opts.image,
			ImagePullPolicy:          corev1.PullIfNotPresent,
			Command:                  []string{"sh", "-c", entrypoint},
			Stdin:                    opts.stdin,
			TTY:                      opts.tty,
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		},
		TargetContainerName: targetName,
	}

	copied := pod.DeepCopy()
	copied.Spec.EphemeralContainers = append(copied.Spec.EphemeralContainers, *ec)

	return copied
}

func waitForContainer(
	ctx context.Context,
	client kubernetes.Interface,
	ns string,
	podName string,
	containerName string,
	running bool,
) (*corev1.Pod, error) {
	ctx, cancel := watchtools.ContextWithOptionalTimeout(ctx, 0*time.Second)
	defer cancel()

	fieldSelector := fields.OneTermEqualSelector("metadata.name", podName).String()
	lw := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.FieldSelector = fieldSelector
			return client.CoreV1().Pods(ns).List(ctx, options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.FieldSelector = fieldSelector
			return client.CoreV1().Pods(ns).Watch(ctx, options)
		},
	}

	ev, err := watchtools.UntilWithSync(ctx, lw, &corev1.Pod{}, nil, func(ev watch.Event) (bool, error) {
		switch ev.Type {
		case watch.Deleted:
			return false, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "")
		}

		p, ok := ev.Object.(*corev1.Pod)
		if !ok {
			return false, fmt.Errorf("watch did not return a pod: %v", ev.Object)
		}

		s := containerStatusByName(p, containerName)
		if s == nil {
			return false, nil
		}

		if s.LastTerminationState.Terminated != nil || s.State.Terminated != nil || (running && s.State.Running != nil) {
			return true, nil
		}

		return false, nil
	})
	if ev != nil {
		return ev.Object.(*corev1.Pod), err
	}

	return nil, err
}

func attachPodDebugger(
	ctx context.Context,
	cli cliutil.CLI,
	opts *options,
	config *restclient.Config,
	client kubernetes.Interface,
	ns string,
	podName string,
	debuggerName string,
) error {
	cli.PrintAux("Waiting for debugger container...\n")
	pod, err := waitForContainer(ctx, client, ns, podName, debuggerName, true)
	if err != nil {
		return fmt.Errorf("error waiting for debugger container: %v", err)
	}

	status := containerStatusByName(pod, debuggerName)
	if status == nil {
		return fmt.Errorf("error getting debugger container %q status: %+v", debuggerName, err)
	}
	if status.State.Terminated != nil {
		dumpDebuggerLogs(ctx, client, ns, podName, debuggerName, cli.OutputStream())

		if status.State.Terminated.Reason == "Completed" {
			return nil
		}

		return fmt.Errorf("debugger container %q terminated: %s - %s (exit code: %d)",
			debuggerName,
			status.State.Terminated.Reason,
			status.State.Terminated.Message,
			status.State.Terminated.ExitCode)
	}

	debuggerContainer := ephemeralContainerByName(pod, debuggerName)
	if debuggerContainer == nil {
		return fmt.Errorf("cannot find debugger container %q in pod %q", debuggerName, podName)
	}

	if opts.tty && !debuggerContainer.TTY {
		opts.tty = false
		if !opts.quiet {
			cli.PrintErr("Warning: Unable to use a TTY - container %s did not allocate one\n", debuggerName)
		}
	} else if !opts.tty && debuggerContainer.TTY {
		// the container was launched with a TTY, so we have to force a TTY here
		// to avoid getting an error "Unrecognized input header"
		opts.tty = true
	}

	cli.PrintAux("Attaching to debugger container...\n")
	cli.PrintAux("If you don't see a command prompt, try pressing enter.\n")
	req := client.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(podName).
		Namespace(ns).
		SubResource("attach").
		VersionedParams(&corev1.PodAttachOptions{
			Container: debuggerName,
			Stdin:     opts.stdin,
			Stdout:    true,
			Stderr:    !opts.tty,
			TTY:       opts.tty,
		}, scheme.ParameterCodec)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// Container is not running anymore, stop streaming.
		_, _ = waitForContainer(ctx, client, ns, podName, debuggerName, false)
		cli.PrintAux("Debugger container %q is not running...\n", debuggerName)
		cancel()

		dumpDebuggerLogs(ctx, client, ns, podName, debuggerName, cli.OutputStream())
	}()

	return stream(ctx, cli, req.URL(), config, opts.tty)
}

func stream(
	ctx context.Context,
	cli cliutil.CLI,
	url *url.URL,
	config *restclient.Config,
	raw bool,
) error {
	var resizeQueue *tty.ResizeQueue
	if raw {
		if cli.OutputStream().IsTerminal() {
			resizeQueue = tty.NewResizeQueue(ctx, cli.OutputStream())
			resizeQueue.Start()
		}

		cli.InputStream().SetRawTerminal()
		cli.OutputStream().SetRawTerminal()
		defer func() {
			cli.InputStream().RestoreTerminal()
			cli.OutputStream().RestoreTerminal()
		}()
	}

	spdyExec, err := remotecommand.NewSPDYExecutor(config, "POST", url)
	if err != nil {
		return fmt.Errorf("cannot create SPDY executor: %w", err)
	}

	websocketExec, err := remotecommand.NewWebSocketExecutor(config, "GET", url.String())
	if err != nil {
		return fmt.Errorf("cannot create WebSocket executor: %w", err)
	}
	exec, err := remotecommand.NewFallbackExecutor(websocketExec, spdyExec, httpstream.IsUpgradeFailure)
	if err != nil {
		return fmt.Errorf("cannot create fallback executor: %w", err)
	}

	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             cli.InputStream(),
		Stdout:            cli.OutputStream(),
		Stderr:            cli.ErrorStream(),
		Tty:               raw,
		TerminalSizeQueue: resizeQueue,
	})
}

func dumpDebuggerLogs(
	ctx context.Context,
	client kubernetes.Interface,
	ns string,
	podName string,
	containerName string,
	out io.Writer,
) error {
	req := client.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		Follow:    false,
	})

	readCloser, err := req.Stream(ctx)
	if err != nil {
		return err
	}
	defer readCloser.Close()

	r := bufio.NewReader(readCloser)
	for {
		bytes, err := r.ReadBytes('\n')
		if _, err := out.Write(bytes); err != nil {
			return err
		}

		if err != nil {
			if err != io.EOF {
				return err
			}
			return nil
		}
	}
}

func ephemeralContainerByName(pod *corev1.Pod, containerName string) *corev1.EphemeralContainer {
	for i := range pod.Spec.EphemeralContainers {
		if pod.Spec.EphemeralContainers[i].Name == containerName {
			return &pod.Spec.EphemeralContainers[i]
		}
	}
	return nil
}

func containerStatusByName(pod *corev1.Pod, containerName string) *corev1.ContainerStatus {
	allContainerStatus := [][]corev1.ContainerStatus{
		pod.Status.InitContainerStatuses,
		pod.Status.ContainerStatuses,
		pod.Status.EphemeralContainerStatuses,
	}
	for _, statuses := range allContainerStatus {
		for i := range statuses {
			if statuses[i].Name == containerName {
				return &statuses[i]
			}
		}
	}
	return nil
}
