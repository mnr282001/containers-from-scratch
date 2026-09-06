# containers-from-scratch

Learning how containers actually work by building a tiny one in Go.

Following along with **[Containers From Scratch • Liz Rice • GOTO 2018](https://www.youtube.com/watch?v=8fi7uSYlOdc)**.

The idea: a container isn't a thing, it's just a normal Linux process that has been
lied to about what it can see. This repo builds up that lie one Linux primitive at a
time, until `go run main.go run /bin/bash` feels like `docker run <image> /bin/bash`.

## The mental model

```
docker run <image> <cmd> <params>
go run main.go run       <cmd> <params>
```

`main.go` dispatches on `os.Args[1]`, so `run` is our stand-in for the Docker CLI.

## What the talk builds, in order

Roughly the order things get added in the video:

1. **`run`** — plain `exec.Command`, wired to the host's stdin/stdout/stderr. Not a
   container yet, just a process launcher.
2. **UTS namespace** (`CLONE_NEWUTS`) — give the process its own hostname, so
   `hostname` inside doesn't rename the host.
3. **The `run` / `child` two-step** — some setup has to happen *inside* the new
   namespaces, so the binary re-executes itself (`/proc/self/exe child ...`) and does
   the rest of the work there.
4. **PID namespace** (`CLONE_NEWPID`) — the process becomes PID 1 in its own tree.
5. **`chroot` / new root filesystem** — swap the root dir so the process sees an
   image's files instead of ours.
6. **Mount namespace + `/proc`** (`CLONE_NEWNS`) — mount a fresh proc so `ps` reports
   the container's processes, not the host's, and unmount it on the way out.
7. **cgroups** — write to `/sys/fs/cgroup/...` to cap resources (the talk uses
   `pids.max` to stop a fork bomb).

## Running it

> **Linux only.** Namespaces, `chroot`, and cgroups are Linux kernel features.
> On macOS this won't build past step 1 (`syscall.SysProcAttr` has no `Cloneflags`),
> and even if it did there's nothing to namespace. Use a Linux VM, a cloud box, or
> run it inside a Linux container.

```sh
go run main.go run echo hello
sudo go run main.go run /bin/bash   # root needed once namespaces/chroot land
```

## Notes to self

- `must(err)` is the talk's deliberate shortcut — real error handling would drown out
  the actual lesson.
- The `child` subcommand is never meant to be typed by a human; it's how the program
  re-enters itself on the other side of the namespace boundary.
- For the `chroot` step you need a root filesystem on disk. The talk extracts one from
  an existing image, e.g. `docker export $(docker create ubuntu) | tar -C ./ubuntu-fs -xf -`.

## Reference

- Video: https://www.youtube.com/watch?v=8fi7uSYlOdc
- `man 7 namespaces`, `man 7 cgroups`
