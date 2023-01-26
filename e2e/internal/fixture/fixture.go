package fixture

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gotest.tools/icmd"

	"github.com/iximiuz/cdebug/pkg/uuid"
)

const (
	ImageNginx = "docker.io/library/nginx:1.23"

	ContainerdCtrNamespace = "cdebug-test-ctr"
	// TODO: ContainerdCRINamespace = "cdebug-test-cri"
)

func ctrCmd(args ...string) icmd.Cmd {
	return icmd.Command(
		"sudo", append([]string{"ctr", "--namespace", ContainerdCtrNamespace}, args...)...,
	)
}

func dockerCmd(args ...string) icmd.Cmd {
	return icmd.Command(
		"sudo", append([]string{"docker"}, args...)...,
	)
}

func nerdctlCmd(args ...string) icmd.Cmd {
	nerdctl, err := exec.LookPath("nerdctl")
	if err != nil {
		panic("cannot find nerdctl")
	}

	return icmd.Command(
		"sudo", append([]string{nerdctl}, args...)...,
	)
}

func ContainerdRunBackground(
	t *testing.T,
	image string,
	flags []string,
	args ...string,
) (string, func()) {
	icmd.RunCmd(ctrCmd("image", "pull", image)).Assert(t, icmd.Success)

	contID := t.Name() + "_" + uuid.ShortID()

	cmd := ctrCmd("run", "-d")
	cmd.Command = append(cmd.Command, flags...)
	cmd.Command = append(cmd.Command, image)
	cmd.Command = append(cmd.Command, contID)
	cmd.Command = append(cmd.Command, args...)

	icmd.RunCmd(cmd).Assert(t, icmd.Success)

	cleanup := func() {
		icmd.RunCmd(ctrCmd("task", "rm", "-f", contID)).Assert(t, icmd.Success)
		icmd.RunCmd(ctrCmd("container", "rm", contID)).Assert(t, icmd.Success)
	}

	return contID, cleanup
}

func DockerRunBackground(
	t *testing.T,
	image string,
	flags []string,
	args ...string,
) (string, func()) {
	cmd := dockerCmd("run", "-d")
	cmd.Command = append(cmd.Command, flags...)
	cmd.Command = append(cmd.Command, image)
	cmd.Command = append(cmd.Command, args...)

	res := icmd.RunCmd(cmd)
	res.Assert(t, icmd.Success)

	contID := strings.TrimSpace(res.Stdout())
	cleanup := func() {
		icmd.RunCmd(dockerCmd("rm", "-f", contID)).Assert(t, icmd.Success)
	}

	return contID, cleanup
}

func DockerBuildLocalImage(
	t *testing.T,
) (string, func()) {
	localImage := "thisimageonlyexistslocally:1.0"

	// Get dirname of current file, assumes that the Dockerfile lives next to this file.
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("Failed to get filename")
	}
	dirname := filepath.Dir(filename)

	cmd := dockerCmd("build", "-t", localImage, dirname)

	res := icmd.RunCmd(cmd)
	res.Assert(t, icmd.Success)

	cleanup := func() {
		icmd.RunCmd(dockerCmd("rmi", localImage)).Assert(t, icmd.Success)
	}

	return localImage, cleanup
}

func NerdctlRunBackground(
	t *testing.T,
	image string,
	flags []string,
	args ...string,
) (string, func()) {
	cmd := nerdctlCmd("run", "-d")
	cmd.Command = append(cmd.Command, flags...)
	cmd.Command = append(cmd.Command, image)
	cmd.Command = append(cmd.Command, args...)

	res := icmd.RunCmd(cmd)
	res.Assert(t, icmd.Success)

	contID := strings.TrimSpace(res.Stdout())
	cleanup := func() {
		icmd.RunCmd(nerdctlCmd("rm", "-f", contID)).Assert(t, icmd.Success)
	}

	return contID, cleanup
}
