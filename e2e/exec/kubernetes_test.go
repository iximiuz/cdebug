package exec

import (
	"strings"
	"testing"
	"text/template"

	"gotest.tools/assert"
	"gotest.tools/assert/cmp"
	"gotest.tools/v3/icmd"

	"github.com/iximiuz/cdebug/e2e/internal/fixture"
	"github.com/iximiuz/cdebug/pkg/uuid"
)

var (
	simplePod = template.Must(template.New("simple-pod").Parse(`---
apiVersion: v1
kind: Pod
metadata:
  name: {{.PodName}}
  namespace: default
spec:
  restartPolicy: Never
  containers:
    - image: {{.Image}}
      imagePullPolicy: IfNotPresent
      name: app
`))
)

func TestExecKubernetesSimple(t *testing.T) {
	podName := "cdebug-" + strings.ToLower(t.Name()) + "-" + uuid.ShortID()
	cleanup := fixture.KubectlApply(t, simplePod, map[string]string{
		"PodName": podName,
		"Image":   fixture.ImageNginx,
	})
	defer cleanup()

	fixture.KubectlWaitFor(t, "pod", podName, "Ready")

	// Exec in the pod
	res := icmd.RunCmd(
		icmd.Command("cdebug", "exec", "-q", "pod/"+podName, "busybox"),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "BusyBox v1"))

	// Exec in the pod's container
	res = icmd.RunCmd(
		icmd.Command("cdebug", "exec", "-q", "pod/"+podName+"/app", "cat", "/etc/os-release"),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
}

func TestExecKubernetesShell(t *testing.T) {
	podName := "cdebug-" + strings.ToLower(t.Name()) + "-" + uuid.ShortID()
	cleanup := fixture.KubectlApply(t, simplePod, map[string]string{
		"PodName": podName,
		"Image":   fixture.ImageNginx,
	})
	defer cleanup()

	fixture.KubectlWaitFor(t, "pod", podName, "Ready")

	res := icmd.RunCmd(
		icmd.Command("cdebug", "exec", "-q", "-i", "pod/"+podName+"/app"),
		icmd.WithStdin(strings.NewReader("echo \"hello $((6*7)) world\"\nexit 0\n")),
	)
	res.Assert(t, icmd.Success)
	assert.Equal(t, res.Stderr(), "")
	assert.Check(t, cmp.Contains(res.Stdout(), "hello 42 world"))
}
