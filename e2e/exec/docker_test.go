package exec

import (
	"strings"
	"testing"

	"gotest.tools/assert"
	"gotest.tools/assert/cmp"
	"gotest.tools/v3/icmd"

	"github.com/iximiuz/cdebug/e2e/internal/fixture"
)

func TestExecDockerSimpleCommand(t *testing.T) {
	targetID, cleanup := fixture.DockerRunBackground(t, fixture.ImageNginx, nil)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command("cdebug", "exec", "--rm", "-q", targetID, "cat", "/etc/os-release"),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
}

func TestExecDockerShell(t *testing.T) {
	targetID, cleanup := fixture.DockerRunBackground(t, fixture.ImageDistrolessNodejs, nil,
		"-e", "setInterval(() => console.log('hello'), 5000);",
	)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command("cdebug", "exec", "--rm", "-q", "-i", targetID),
		icmd.WithStdin(strings.NewReader("echo \"hello $((6*7)) world\"\nexit 0\n")),
	)

	res.Assert(t, icmd.Success)
	assert.Equal(t, res.Stderr(), "")
	assert.Check(t, cmp.Contains(res.Stdout(), "hello 42 world"))
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

func TestExecDockerUseLocalImage(t *testing.T) {
	targetID, targetCleanup := fixture.DockerRunBackground(t, fixture.ImageNginx, nil)
	defer targetCleanup()

	remoteImage := "busybox:musl"
	fixture.DockerImageRemove(t, remoteImage)

	res := icmd.RunCmd(
		icmd.Command("cdebug", "exec", "--rm", "-i", "--image", remoteImage, targetID, "cat", "/etc/os-release"),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
	assert.Check(t, cmp.Contains(res.Stderr(), "Pulling debugger image..."))

	localImage, imageCleanup := fixture.DockerImageBuild(t, "thisimageonlyexistslocally:1.0")
	defer imageCleanup()

	res = icmd.RunCmd(
		icmd.Command("cdebug", "exec", "--rm", "-i", "--image", localImage, targetID, "cat", "/etc/os-release"),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
	assert.Equal(t, strings.Contains(res.Stderr(), "Pulling debugger image..."), false)
}

func TestExecCdebugRootfsEnvVar(t *testing.T) {
	targetID, cleanup := fixture.DockerRunBackground(t, fixture.ImageNginx, nil)
	defer cleanup()

	cmd := icmd.Command("cdebug", "exec", "--rm", "-q", targetID, "echo", "$CDEBUG_ROOTFS")
	res := icmd.RunCmd(cmd)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "/.cdebug-"))
}
