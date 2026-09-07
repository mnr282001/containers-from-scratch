# containers-from-scratch

A container, built by hand in Go out of nothing but Linux primitives.

Started as a walkthrough of **[Containers From Scratch • Liz Rice • GOTO 2018](https://www.youtube.com/watch?v=8fi7uSYlOdc)**,
then rewritten once the talk was finished — same lesson, none of the shortcuts.

The idea: a container isn't a thing. It's a normal Linux process that has been lied
to about what it can see. This repo tells that lie one primitive at a time, until
`./cfs run -rootfs ./ubuntu-fs /bin/bash` feels like `docker run <image> /bin/bash`.

## Build

There is no `cfs` on your PATH until you make one — the binary is named after
whatever you build it as:

```sh
go build -o cfs .     # ./cfs
```

`sudo ./cfs ...` below, or `sudo install cfs /usr/local/bin/` once if you want to
drop the `./`. **On macOS the build succeeds but the binary just explains itself
and exits 1** — you need a Linux host to actually run it.

## Usage

```
docker run <image>  <cmd> <params>
./cfs      run [-flags] <cmd> <params>
```

```sh
sudo ./cfs run echo hello
sudo ./cfs run -rootfs ./ubuntu-fs /bin/bash
sudo ./cfs run -pids 20 -memory 100M -rootfs ./ubuntu-fs /bin/bash
```

`go run . run ...` works too, but `sudo go run` builds as root and scribbles in
root's build cache, so prefer building first.

| flag        | default       | what it does                                    |
| ----------- | ------------- | ----------------------------------------------- |
| `-rootfs`   | *(host root)* | directory to `chroot` into                      |
| `-hostname` | `container`   | hostname inside the UTS namespace               |
| `-memory`   | `100M`        | `memory.max`, or `max` to leave it uncapped     |
| `-pids`     | `20`          | `pids.max`, or `max` to leave it uncapped       |

You need a root filesystem on disk for `-rootfs`. Steal one from an image:

```sh
mkdir ubuntu-fs && docker export "$(docker create ubuntu)" | tar -C ubuntu-fs -xf -
```

> **Linux only, and root.** On macOS the binary prints an explanation and exits.
> The Linux code still type-checks from a Mac with `GOOS=linux go build ./...`
> (`.vscode/settings.json` points gopls at Linux for the same reason).

## How it works

`main` dispatches on `os.Args[1]`. `run` is our stand-in for the Docker CLI —
but most of the setup has to happen *inside* the new namespaces, and you can't
be inside a namespace you haven't created yet. So `run` re-executes this same
binary as `child` (`/proc/self/exe child ...`) with the clone flags applied, and
`child` does the rest on the far side of the boundary. `child` is plumbing; you
never type it.

```
run                              child
 ├─ validate flags                ├─ join the cgroup
 ├─ create cgroup + limits        ├─ sethostname
 ├─ clone(NEWUTS|NEWPID|NEWNS) ──▶├─ chroot + chdir /
 ├─ wait                          ├─ mount /proc
 └─ rmdir cgroup                  └─ exec the command
```

What each piece buys you:

- **`CLONE_NEWUTS`** — its own hostname, so `hostname` inside doesn't rename the host.
- **`CLONE_NEWPID`** — it becomes PID 1 in its own process tree.
- **`CLONE_NEWNS`** + `Unshareflags` — its own mount table, marked `MS_PRIVATE`, so
  mounting `/proc` can't propagate back to the host.
- **`chroot`** — it sees the image's files instead of ours.
- **fresh `/proc`** — `ps` reports the container's PID namespace, not the host's.
- **cgroup v2** — `pids.max` stops a fork bomb, `memory.max` caps the damage.

Config crosses the `run`→`child` re-exec through `CFS_*` environment variables,
which keeps `child`'s argv equal to the command the user actually asked for. They
are stripped again before the contained command runs.

## What changed from the talk version

The talk optimizes for fitting on a slide. This optimizes for being correct:

- **`must(err)` is gone.** Errors are returned and wrapped, and the container's
  exit status is passed through instead of collapsing into a panic — `run echo` and
  `run false` now differ the way you'd expect.
- **The rootfs path is a flag,** not a hardcoded `/home/<someone>/ubuntu-fs`.
- **The cgroup gets cleaned up.** Each run makes its own `cfs-<pid>` leaf and rmdirs
  it afterwards, instead of every run reusing (and leaking) `/sys/fs/cgroup/container`.
- **Controllers get delegated.** Writing `+pids +memory` to the parent's
  `cgroup.subtree_control` first — without it, `pids.max` doesn't exist in the leaf
  on plenty of hosts and the write fails with ENOENT.
- **`mount` errors are checked,** and the unmount runs on a `defer` so it still
  happens when the command fails.
- **It builds on macOS.** `main.go` is `//go:build linux`; `main_unsupported.go`
  explains itself and exits non-zero everywhere else.

## Reference

- Video: https://www.youtube.com/watch?v=8fi7uSYlOdc
- `man 7 namespaces`, `man 7 cgroups`, `man 2 clone`
