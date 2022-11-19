# cdebug - a swiss army knife of container debugging (WIP)

TODO: Add table command (exec, port-forward, export, explore) x runtime (docker, containerd, k8s, etc).

A handy way to troubleshoot containers lacking a shell and/or debugging tools
(e.g, scratch, slim, or distroless):

```sh
# Simple (busybox)
$ cdebug exec -it <target-container>

# Advanced (shell + ps + vim)
$ cdebug exec -it --image nixery.dev/shell/ps/vim <target-container>
```

The `cdebug exec` command is some sort of crossbreeding of `docker exec` and `kubectl debug` commands.
You point the tool at a running container, say what toolkit image to use, and it starts
a debugging "sidecar" container that _feels_ like a `docker exec` session into the target container:

- The root filesystem of the debugger **_is_** the root filesystem of the target container.
- The target container isn't recreated and/or restarted.
- No extra volumes or copying of debugging tools is needed.
- The debugging tools **_are_** available in the target container.

Currently supported toolkit images:

- 🧰 `busybox` - a good default choice
- 🧙 `nixery.dev/shell/...` - [a very powerful way to assemble images on the fly](https://nixery.dev/).

Supported runtimes:

- Docker (via the socket file)
- containerd (via the socket file) - coming soon
- Kubernetes CRI (via the CRI gRPC API) - coming later
- Kubernetes (via the API server) - coming later
- runc or alike (via directly invoking the CLI) - coming later.

## Installation

It's a statically linked Go binary, so you know the drill:

```sh
GOOS=linux
GOARCH=amd64

curl -Ls https://github.com/iximiuz/cdebug/releases/latest/download/cdebug_${GOOS}_${GOARCH}.tar.gz | tar xvz

sudo mv cdebug /usr/local/bin
```

At the moment, the following targets are (kinda sorta) supported:

- linux/amd64
- darwin/amd64
- darwin/arm64

## Demo 1: An interactive shell with busybox

First, a target container is needed. Let's use a distroless nodejs image for that:

```sh
docker run -d --rm \
  --name my-distroless gcr.io/distroless/nodejs \
  -e 'setTimeout(() => console.log("Done"), 99999999)'
```

Now, let's start an interactive shell (using busybox) into the above container:

```sh
cdebug exec -it my-distroless
```

Exploring the filesystem shows that it's a rootfs of the nodejs container:

```sh
/ $# ls -lah
total 60K
drwxr-xr-x    1 root     root        4.0K Oct 17 23:49 .
drwxr-xr-x    1 root     root        4.0K Oct 17 23:49 ..
👉 lrwxrwxrwx 1 root     root          18 Oct 17 23:49 .cdebug-c153d669 -> /proc/55/root/bin/
-rwxr-xr-x    1 root     root           0 Oct 17 19:49 .dockerenv
drwxr-xr-x    2 root     root        4.0K Jan  1  1970 bin
drwxr-xr-x    2 root     root        4.0K Jan  1  1970 boot
drwxr-xr-x    5 root     root         340 Oct 17 19:49 dev
drwxr-xr-x    1 root     root        4.0K Oct 17 19:49 etc
drwxr-xr-x    3 nonroot  nonroot     4.0K Jan  1  1970 home
drwxr-xr-x    1 root     root        4.0K Jan  1  1970 lib
drwxr-xr-x    2 root     root        4.0K Jan  1  1970 lib64
drwxr-xr-x    5 root     root        4.0K Jan  1  1970 nodejs
...
```

Notice 👉  above - that's where the debugging tools live:

```sh
/ $# echo $PATH
/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/.cdebug-c153d669
```

The process tree of the debugger container is the process tree of the target:

```sh
/ # ps auxf
PID   USER     TIME  COMMAND
    1 root      0:00 /nodejs/bin/node -e setTimeout(() => console.log("Done"),
   13 root      0:00 sh -c  set -euo pipefail  sleep 999999999 & SANDBOX_PID=$!
   19 root      0:00 sleep 999999999
   21 root      0:00 sh
   28 root      0:00 [sleep]
   39 root      0:00 [sleep]
   45 root      0:00 ps auxf
```

## Demo 2: An interactive shell with code editor

If the tools provided by busybox aren't enough, you can bring your own tools with
a ~~little~~ huge help of the [nixery](https://nixery.dev/) project:

```sh
cdebug exec -it --image nixery.dev/shell/vim my-distroless
```

## Demo 3: An interactive shell with tshark and other advanced tools

Even more powerful exammple:

```sh
cdebug exec -it --image nixery.dev/shell/ps/findutils/tshark my-distroless
```

## How it works

The technique is based on the ideas from this [blog post](https://iximiuz.com/en/posts/docker-debug-slim-containers).
Oversimplifying, the debugger container is started like:

```sh
docker run [-it] \
  --network container:<target> \
  --pid container:<target> \
  --uts container:<target> \
  <toolkit-image>
  sh -c <<EOF
ln -s /proc/$$/root/bin/ /proc/1/root/.cdebug

export PATH=$PATH:/.cdebug
chroot /proc/1/root sh
EOF
```

The secret sauce is the symlink + PATH modification + chroot-ing.

## F.A.Q

**Q:** Running `cdebug exec` fails with `rm: cannot remove '/proc/1/root/nix': Permission denied` or
`ln: /proc/1/root/.cdebug-XXXXXXXX: Permission denied`.

Chances are your target container has been started with elevated permissions while you're trying to run a non-privileged debugger sidecar. Try `cdebug exec --privileged` instead.

## Similar tools

- [`docker-slim debug`](https://github.com/docker-slim/docker-slim) - a PoC `debug` command for DockerSlim (contributed by [D4N](https://github.com/D4N))
- [`debug-ctr`](https://github.com/felipecruz91/debug-ctr) - a debugger that creates a new container out of the original container with the toolkit mounted in a volume (by [Felipe Cruz Martinez](https://github.com/felipecruz91))
- [`docker-debug`](https://github.com/zeromake/docker-debug) - much like `cdebug exec` but without the chroot trick.
- [`docker-opener`](https://github.com/artemkaxboy/docker-opener) - a multi-purpose tool that in particular can run a shell session into your container (and if there is no shell inside, it'll bring its own busybox).
- [`cntr`](https://github.com/Mic92/cntr) - is "a replacement for `docker exec` that brings all your developers tools with you" by mounting the file system from one container (or the host) into the target container and creating a nested container with the help of a FUSE filesystem. Supports a huge range of runtimes (docker, podman, LXC/LXD, rkt, systemd-nspawn, containerd) because it operates on the OS level.

## TODO:

- Make exec accept (partial) container IDs (only names are supported at the moment)
- Terminal resizing ([example](https://github.com/docker/cli/blob/110c4d92b883357c9fb3edc344c4fbec5f77896f/cli/command/container/tty.go#L71))
- More `exec` flags (like in `docker run`): `--cap-add`, `--cap-drop`, `--env`, `--volume`, etc.
- Helper command(s) suggesting nix(ery) packages
- E2E Tests
- Cross-platform builds + goreleaser
- Non-docker runtimes (containerd, runc, k8s)

## Contributions

It's a pre-alpha with no sound design yet, so I may not be accepting all PRs. Sorry about that :)
