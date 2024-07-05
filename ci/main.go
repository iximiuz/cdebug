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
	"strings"
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
	return dag.Go().Build(src, GoBuildOpts{
		Static: true,
	}).File("cdebug")
	// return dag.Container().
	// From("golang:1.22-alpine").
	// WithDirectory("/app", src).
	// WithWorkdir("/app").
	// WithEnvVariable("CGO_ENABLED", "0").
	// WithExec([]string{"go", "build", "-o", "cdebug"}).
	// File("cdebug")
}

// socat TCP-LISTEN:2375,reuseaddr,fork UNIX-CONNECT:/var/run/docker.sock &
// socat TCP-LISTEN:2376,reuseaddr,fork UNIX-CONNECT:/var/run/containerd/containerd.sock &
// dagger call test --src .. --docker tcp://127.0.0.1:2375 --containerd tcp://127.0.0.1:2376

// Runs the e2e tests for the project.
func (m *Ci) Test(ctx context.Context, src *Directory, docker *Service, containerd *Service) error {
	// cdebug := m.Build(ctx, src)
	cdebug := src.File("cdebug")

	ctr := dag.Container().
		From("docker:dind").
		File("/usr/local/bin/ctr")

	runVolume := dag.CacheVolume("run")

	// docker := dag.
	// 	Container().
	// 	From("docker:dind").
	// 	WithoutEntrypoint().
	// 	WithExposedPort(2375).
	// 	WithMountedCache("/run", runVolume).
	// 	WithExec([]string{
	// 		"dockerd",
	// 		"--host=tcp://0.0.0.0:2375",
	// 		"--host=unix:///run/docker.sock",
	// 		"--tls=false",
	// 		"--iptables=false",
	// 	}, ContainerWithExecOpts{
	// 		InsecureRootCapabilities: true,
	// 	}).
	// 	AsService()

	containerdEndpoint, err := containerd.Endpoint(ctx, ServiceEndpointOpts{
		Scheme: "tcp",
	})
	if err != nil {
		return err
	}

	// socat UNIX-LISTEN:/var/run/usbmuxd,mode=777,reuseaddr,fork TCP:10.16.89.10:10015
	containerdProxy := dag.
		Container().
		From("alpine").
		WithMountedCache("/run", runVolume).
		WithExec([]string{"apk", "add", "--no-cache", "socat"}).
		WithExec([]string{"mkdir", "-p", "/run/containerd"}).
		WithExec([]string{"socat", "UNIX-LISTEN:/run/containerd/containerd.sock,mode=777,reuseaddr,fork", "TCP:" + strings.TrimPrefix(containerdEndpoint, "tcp://")}).
		AsService()

	if _, err := containerdProxy.Start(ctx); err != nil {
		return err
	}
	defer containerdProxy.Stop(ctx)

	container := m.testBase(ctx).
		WithDirectory("/app", src).
		WithFile("/usr/local/bin/cdebug", cdebug).
		WithFile("/usr/local/bin/ctr", ctr).
		WithWorkdir("/app").
		WithServiceBinding("docker", docker).
		WithServiceBinding("containerd", containerd).
		WithEnvVariable("DOCKER_HOST", "tcp://docker:2375").
		// WithEnvVariable("CONTAINERD_ADDRESS", "containerd:2376").
		WithMountedCache("/run", runVolume).
		WithMountedTemp("/tmp").
		// WithExec([]string{"kind", "-v", "999", "create", "cluster"}).
		WithExec([]string{"go", "test", "-v", "-count", "1", "./e2e/exec"})

	_, err = container.Sync(ctx)
	return err
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
