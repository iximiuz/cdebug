package exec

import (
	"fmt"
	"regexp"
	"testing"

	"gotest.tools/assert"
	"gotest.tools/assert/cmp"
	"gotest.tools/v3/icmd"

	"github.com/iximiuz/cdebug/e2e/internal/fixture"
)

func TestExecDockerSimple(t *testing.T) {
	targetID, cleanup := fixture.DockerRunBackground(t, fixture.ImageNginx, nil)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command("cdebug", "exec", "--rm", "-q", targetID, "cat", "/etc/os-release"),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
}

func TestExecDockerUseLocalImage(t *testing.T) {
	localImage, cleanupLocalImage := fixture.DockerBuildLocalImage(t)
	defer cleanupLocalImage()

	targetImageID, targetImageCleanup := fixture.DockerRunBackground(t, fixture.ImageNginx, nil)
	defer targetImageCleanup()

	res := icmd.RunCmd(
		icmd.Command("cdebug", "--image", localImage, "-l=debug", "exec", "--rm", "-q", targetImageID, "cat", "/etc/os-release"),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
	assert.Assert(t, func() cmp.Result {
		re := regexp.MustCompile("Pulling debugger image...")
		if re.MatchString(res.Stdout()) {
			return cmp.ResultFailure(fmt.Sprintf("Image %s shouldn't be pulled because it only exists locally", localImage))
		}
		return cmp.ResultSuccess
	})
}

func TestExecDockerHostNamespaces(t *testing.T) {
	targetID, cleanup := fixture.DockerRunBackground(t, fixture.ImageNginx,
		[]string{"--net", "host", "--pid", "host"},
	)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command("cdebug", "exec", "--rm", "-q", targetID, "cat", "/etc/os-release"),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
}

func TestExecDockerRunAsUser(t *testing.T) {
	targetID, cleanup := fixture.DockerRunBackground(t, fixture.ImageNginxUnprivileged, nil)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command("cdebug", "exec", "--rm", "-q", "-u", "101:101", targetID, "id", "-u"),
	)
	res.Assert(t, icmd.Success)
	assert.Equal(t, res.Stderr(), "")
	assert.Check(t, cmp.Contains(res.Stdout(), "101"))

	res = icmd.RunCmd(
		icmd.Command("cdebug", "exec", "--rm", "-q", "-u", "101:101", targetID, "busybox"),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "BusyBox v1"))
}

func TestExecDockerNixery(t *testing.T) {
	targetID, cleanup := fixture.DockerRunBackground(t, fixture.ImageNginx, nil)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command(
			"cdebug", "exec", "--rm", "-q",
			"--image", "nixery.dev/shell/vim",
			targetID,
			"vim", "--version",
		),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "VIM - Vi IMproved"))
}
