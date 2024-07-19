package fixture

import (
	"os/exec"
	"strings"
	"testing"
	"text/template"

	"gotest.tools/icmd"

	"github.com/iximiuz/cdebug/pkg/uuid"
)

const (
	ImageDistrolessNodejs  = "gcr.io/distroless/nodejs20-debian12"
	ImageNginx             = "docker.io/library/nginx:1.25"
	ImageNginxUnprivileged = "docker.io/nginxinc/nginx-unprivileged:1.25"

	ContainerdCtrNamespace = "cdebug-test-ctr"
	// TODO: ContainerdCRINamespace = "cdebug-test-cri"
)

func ctrCmd(args ...string) icmd.Cmd {
	return icmd.Command(
		"ctr", append([]string{"--namespace", ContainerdCtrNamespace}, args...)...,
	)
}

func dockerCmd(args ...string) icmd.Cmd {
	return icmd.Command(
		"docker", args...,
	)
}

func nerdctlCmd(args ...string) icmd.Cmd {
	nerdctl, err := exec.LookPath("nerdctl")
	if err != nil {
		panic("cannot find nerdctl")
	}

	return icmd.Command(
		"sudo", append([]string{"-E", nerdctl}, args...)...,
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

func DockerImageBuild(
	t *testing.T,
	name string,
) (string, func()) {
	cmd := dockerCmd("build", "-t", name, "-")

	res := icmd.RunCmd(cmd, icmd.WithStdin(strings.NewReader("FROM busybox:musl\n")))
	res.Assert(t, icmd.Success)

	cleanup := func() {
		icmd.RunCmd(dockerCmd("rmi", name)).Assert(t, icmd.Success)
	}

	return name, cleanup
}

func DockerImageRemove(
	t *testing.T,
	name string,
) {
	res := icmd.RunCmd(dockerCmd("rmi", name))
	if !strings.Contains(res.Stderr(), "No such image") {
		res.Assert(t, icmd.Success)
	}
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

func KubectlApply(
	t *testing.T,
	manifestTmpl *template.Template,
	data interface{},
) func() {
	var buf strings.Builder
	if err := manifestTmpl.Execute(&buf, data); err != nil {
		t.Fatalf("cannot execute template: %v", err)
	}

	manifest := buf.String()

	cmd := icmd.Command("kubectl", "apply", "-f", "-")
	res := icmd.RunCmd(cmd, icmd.WithStdin(strings.NewReader(manifest)))
	res.Assert(t, icmd.Success)

	return func() {
		cmd := icmd.Command("kubectl", "delete", "-f", "-")
		res := icmd.RunCmd(cmd, icmd.WithStdin(strings.NewReader(manifest)))
		res.Assert(t, icmd.Success)
	}
}

func KubectlWaitFor(
	t *testing.T,
	kind string,
	name string,
	condition string,
) {
	cmd := icmd.Command("kubectl", "wait", kind, name, "--for=condition="+condition, "--timeout=60s")
	res := icmd.RunCmd(cmd)
	res.Assert(t, icmd.Success)
}
