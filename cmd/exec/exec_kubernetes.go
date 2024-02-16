package exec

import (
	"context"
	"errors"
	"fmt"

	"github.com/iximiuz/cdebug/pkg/cliutil"
)

func runDebuggerKubernetes(ctx context.Context, cli cliutil.CLI, opts *options) error {
	if opts.autoRemove {
		return fmt.Errorf("--rm flag is not supported for Kubernetes")
	}

	return errors.New("not implemented")
}

// func runDebuggerEphemeralContainers(ctx context.Context, o *options) error {
// 	podJS, err := json.Marshal(pod)
// 	if err != nil {
// 		return nil, "", fmt.Errorf("error creating JSON for pod: %v", err)
// 	}
//
// 	debugPod, debugContainer, err := o.generateDebugContainer(pod)
// 	if err != nil {
// 		return nil, "", err
// 	}
//
// 	debugJS, err := json.Marshal(debugPod)
// 	if err != nil {
// 		return nil, "", fmt.Errorf("error creating JSON for debug container: %v", err)
// 	}
//
// 	patch, err := strategicpatch.CreateTwoWayMergePatch(podJS, debugJS, pod)
// 	if err != nil {
// 		return nil, "", fmt.Errorf("error creating patch to add debug container: %v", err)
// 	}
//
// 	pods := o.podClient.Pods(pod.Namespace)
// 	result, err := pods.Patch(ctx, pod.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}, "ephemeralcontainers")
// 	if err != nil {
// 		// The apiserver will return a 404 when the EphemeralContainers feature is disabled because the `/ephemeralcontainers` subresource
// 		// is missing. Unlike the 404 returned by a missing pod, the status details will be empty.
// 		if serr, ok := err.(*errors.StatusError); ok && serr.Status().Reason == metav1.StatusReasonNotFound && serr.ErrStatus.Details.Name == "" {
// 			return nil, "", fmt.Errorf("ephemeral containers are disabled for this cluster (error from server: %q)", err)
// 		}
//
// 		return nil, "", err
// 	}
//
// 	return result, debugContainer.Name, nil
// }
//
