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
)

type Ci struct{}

// GIT_COMMIT=$(shell git rev-parse --verify HEAD)
// UTC_NOW=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
//
// build-dev:
// 	go build \
// 		-ldflags="-X 'main.version=dev' -X 'main.commit=${GIT_COMMIT}' -X 'main.date=${UTC_NOW}'" \
// 		-o cdebug

func (m *Ci) Build(ctx context.Context, src *Directory) *File {
	return dag.Go().FromVersion("1.22-alpine").Build(src, GoBuildOpts{
		Static: true,
	}).File("cdebug")
}

// socat TCP-LISTEN:2375,reuseaddr,fork UNIX-CONNECT:/var/run/docker.sock &
// socat TCP-LISTEN:2376,reuseaddr,fork UNIX-CONNECT:/var/run/containerd/containerd.sock &
// dagger call test --src .. --docker tcp://127.0.0.1:2375 --containerd tcp://127.0.0.1:2376
// Runs the e2e tests for the project.
func (m *Ci) Test(ctx context.Context, src *Directory) (*Container, error) {
	cdebug := m.Build(ctx, src)

	docker := dag.
		Container().
		From("docker:dind").
		WithoutEntrypoint().
		WithExposedPort(2375).
		WithMountedCache("/var/lib/docker", dag.CacheVolume("docker-lib"))

	dockerCli, err := docker.File("/usr/local/bin/docker").Sync(ctx)
	if err != nil {
		return nil, err
	}

	docker = docker.
		WithExec([]string{
			"dockerd",
			"--host=tcp://0.0.0.0:2375",
			"--tls=false",
		}, ContainerWithExecOpts{
			InsecureRootCapabilities: true,
		})

	return dag.Go().
		FromVersion("1.22-alpine").
		Base().
		WithFile("/usr/local/bin/cdebug", cdebug).
		WithFile("/usr/local/bin/docker", dockerCli).
		WithDirectory("/app/cdebug", src).
		WithWorkdir("/app/cdebug").
		WithServiceBinding("docker", docker.AsService()).
		WithEnvVariable("DOCKER_HOST", "tcp://docker:2375").
		WithExec([]string{"go", "test", "-run=TestExecDockerSimpleCommand", "./e2e/exec"}), nil
}

func (m *Ci) testBase(ctx context.Context) *Container {
	return dag.Container().
		From("golang:1.22-alpine").
		WithExec([]string{"apk", "add", "--no-cache", "sudo", "docker", "kubectl", "nerdctl"})
	// WithExec([]string{"go", "install", "sigs.k8s.io/kind@v0.22.0"})
	// WithFile("/usr/local/bin/kind", dag.HTTP("https://kind.sigs.k8s.io/dl/v0.22.0/kind-linux-amd64"), ContainerWithFileOpts{
	// 	Permissions: 0777,
	// })
}
