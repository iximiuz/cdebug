# cdebug - experimental container debugger

Work in progres...

## Demo

The command is very similar to `docker exec`. You point it to the target container,
potentially ask the session to be interactive (`-it`), and specify the debugging
toolkit image (`busybox` or anything starting from `nixery.dev/shell`).

**Important:** The target container isn't recreated and/or restarted. And no extra
volumes is needed.

Notice how the debugger's shell actually has the original distroless rootfs as it's root directory:

```sh
$ docker run -d --rm \
  --name my-distroless gcr.io/distroless/nodejs \
  -e 'setTimeout(() => console.log("Done"), 99999999)'

$ go run main.go exec -it my-distroless
{"status":"Pulling from library/busybox","id":"latest"}
{"status":"Digest: sha256:9810966b5f712084ea05bf28fc8ba2c8fb110baa2531a10e2da52c1efc504698"}
{"status":"Status: Image is up to date for busybox:latest"}
+ rm -rf /proc/1/root/.cdebug
+ ln -s /proc/55/root/bin/ /proc/1/root/.cdebug
+ export 'PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/.cdebug'
+ chroot /proc/1/root sh
/ # ls -lah
total 60K
drwxr-xr-x    1 root     root        4.0K Oct 17 23:49 .
drwxr-xr-x    1 root     root        4.0K Oct 17 23:49 ..
lrwxrwxrwx    1 root     root          18 Oct 17 23:49 .cdebug -> /proc/55/root/bin/
-rwxr-xr-x    1 root     root           0 Oct 17 19:49 .dockerenv
drwxr-xr-x    2 root     root        4.0K Jan  1  1970 bin
drwxr-xr-x    2 root     root        4.0K Jan  1  1970 boot
drwxr-xr-x    5 root     root         340 Oct 17 19:49 dev
drwxr-xr-x    1 root     root        4.0K Oct 17 19:49 etc
drwxr-xr-x    3 nonroot  nonroot     4.0K Jan  1  1970 home
drwxr-xr-x    1 root     root        4.0K Jan  1  1970 lib
drwxr-xr-x    2 root     root        4.0K Jan  1  1970 lib64
drwxr-xr-x    5 root     root        4.0K Jan  1  1970 nodejs
dr-xr-xr-x  191 root     root           0 Oct 17 19:49 proc
drwx------    1 root     root        4.0K Oct 17 19:55 root
drwxr-xr-x    2 root     root        4.0K Jan  1  1970 run
drwxr-xr-x    2 root     root        4.0K Jan  1  1970 sbin
dr-xr-xr-x   13 root     root           0 Oct 17 19:49 sys
drwxrwxrwt    2 root     root        4.0K Jan  1  1970 tmp
drwxr-xr-x    1 root     root        4.0K Jan  1  1970 usr
drwxr-xr-x    1 root     root        4.0K Jan  1  1970 var
```
