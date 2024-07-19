// A generated module for Ci functions

//
// This module has been generated via dagger init and serves as a reference to
// basic module structure as you get started with Dagger.
//
// Two functions have been pre-created. You can modify, delete, or add to them,
// as needed. They demonstrate usage of arguments and return types using simple
// echo and grep commands. The functions can be called from the dagger CLI or
// from one of the SDKs.
//
// The first line in this comment block is a short description line and the
// rest is a long description with more detail on the module's purpose or usage,
// if appropriate. All modules should have a short description.

package main

import (
	"context"
	"dagger/ci/internal/dagger"
	"fmt"
)

type Ci struct{}

func (m *Ci) Build(ctx context.Context, src *dagger.Directory) *dagger.File {
	return dag.Go().FromVersion("1.22").Build(src, dagger.GoBuildOpts{
		Static: true,
	}).File("cdebug")
}

func (m *Ci) TestExec(ctx context.Context,
	src *dagger.Directory,
	// +optional
	// +default="docker"
	tool string,
) (*dagger.Container, error) {
	if tool != "docker" && tool != "kubernetes" && tool != "containerd" && tool != "nerdctl" {
		return nil, fmt.Errorf("tool %s is not supported. Supported values are: kubernetes,containerd,nerdctl,docker")
	}

	if tool != "docker" {
		return nil, fmt.Errorf("tool %s is no yet implemented", tool)
	}

	switch tool {
	case "docker":
		return m.TestDockerExec(ctx, src)
	case "containerd":
		return m.TestContainerdExec(ctx, src)
	default:
		return nil, fmt.Errorf("tool %s is no yet implemented", tool)
	}
}

func (m *Ci) TestContainerdExec(ctx context.Context, src *dagger.Directory) (*dagger.Container, error) {
	cdebug := m.Build(ctx, src)

	containerd := dag.
		Container().
		From("tianon/containerd")

	return dag.Go().
		FromVersion("1.22").
		Base().
		WithDirectory("/usr/local/bin", containerd.Directory("/usr/local/bin")).
		WithFile("/usr/local/bin/cdebug", cdebug).
		WithDirectory("/app/cdebug", src).
		WithWorkdir("/app/cdebug").
		WithMountedTemp("/var/lib/containerd").
		WithExec([]string{"sh", "-c", `
docker-entrypoint.sh containerd &
sleep 3
ctr i pull docker.io/library/hello-world:latest
ctr run docker.io/library/hello-world:latest foo
	 `}, dagger.ContainerWithExecOpts{InsecureRootCapabilities: true}), nil
}

func (m *Ci) TestDockerExec(ctx context.Context, src *dagger.Directory) (*dagger.Container, error) {
	cdebug := m.Build(ctx, src)

	docker := dag.
		Container().
		From("docker:dind").
		WithoutEntrypoint().
		WithExposedPort(2375).
		WithMountedCache("/var/lib/docker", dag.CacheVolume("docker-lib"))

	//dockerCli, err := docker.File("/usr/local/bin/docker").Sync(ctx)
	//if err != nil {
	//return nil, err
	//}

	docker = docker.
		WithEnvVariable("DOCKER_TLS_CERTDIR", "").
		WithExec([]string{
			"dockerd-entrypoint.sh",
		}, dagger.ContainerWithExecOpts{
			InsecureRootCapabilities: true,
		})

	return dag.Go().
		FromVersion("1.22-alpine").
		Base().
		WithFile("/usr/local/bin/cdebug", cdebug).
		// WithFile("/usr/local/bin/docker", dockerCli).
		WithDirectory("/app/cdebug", src).
		WithWorkdir("/app/cdebug").
		WithServiceBinding("docker", docker.AsService()).
		WithEnvVariable("DOCKER_HOST", "tcp://docker:2375").
		WithExec([]string{"go", "test", "-v", "./e2e/exec/docker_test.go"}), nil
}
