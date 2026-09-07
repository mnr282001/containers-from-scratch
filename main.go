//go:build linux

// Command cfs runs a process inside a container that it builds by hand out of
// Linux primitives: namespaces for isolation, chroot for the filesystem, and a
// cgroup for resource limits. It is the Liz Rice "Containers From Scratch" talk
// with the shortcuts taken out.
//
// The shape of the CLI mirrors Docker's:
//
//	docker run <image>  <cmd> <params>
//	cfs        run [-flags] <cmd> <params>
//
// A container needs setup that can only happen *inside* the new namespaces
// (hostname, chroot, /proc), so `run` re-executes this same binary as `child`
// on the far side of the namespace boundary. `child` is not meant to be typed
// by a human.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// cgroupRoot is the mount point of the cgroup v2 unified hierarchy.
const cgroupRoot = "/sys/fs/cgroup"

// Configuration crosses the run -> child re-exec through the environment, which
// keeps the child's argv equal to the command the user asked for.
const (
	envRootfs   = "CFS_ROOTFS"
	envHostname = "CFS_HOSTNAME"
	envCgroup   = "CFS_CGROUP"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "child":
		err = child(os.Args[2:])
	case "help", "-h", "--help":
		usage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "cfs: unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}

	if err != nil {
		// The container's exit status is the whole point of running it, so pass
		// it up instead of collapsing every failure into 1.
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code := exit.ExitCode()
			if code < 0 {
				code = 1 // killed by a signal
			}
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, "cfs:", err)
		os.Exit(1)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `cfs — a container, assembled by hand from Linux primitives.

usage:
  cfs run [flags] <cmd> [args...]

flags:
  -rootfs DIR    chroot into DIR (default: keep the host root)
  -hostname NAME hostname inside the UTS namespace (default "container")
  -memory LIMIT  memory.max for the container, e.g. 100M or "max" (default "100M")
  -pids LIMIT    pids.max for the container, e.g. 20 or "max" (default "20")

examples:
  sudo cfs run echo hello
  sudo cfs run -rootfs ./ubuntu-fs /bin/bash

Requires root, Linux, and cgroup v2 mounted at `+cgroupRoot+`.
`)
}

// config is everything the parent decides and the child carries out.
type config struct {
	rootfs   string
	hostname string
	memory   string
	pids     string
}

// run is the parent half: it validates the request, builds the cgroup, and
// starts this binary again as `child` inside fresh namespaces.
func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Usage = func() { usage(os.Stderr) }

	var cfg config
	fs.StringVar(&cfg.rootfs, "rootfs", "", "directory to chroot into")
	fs.StringVar(&cfg.hostname, "hostname", "container", "hostname inside the container")
	fs.StringVar(&cfg.memory, "memory", "100M", "memory.max for the container")
	fs.StringVar(&cfg.pids, "pids", "20", "pids.max for the container")

	// Parsing stops at the first non-flag argument, so everything from the
	// command onwards belongs to the container, not to us.
	if err := fs.Parse(args); err != nil {
		return err
	}
	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		return errors.New("run: no command given")
	}

	if cfg.rootfs != "" {
		// Resolve now, while we can still see the host's filesystem the way the
		// user typed it; the child gets an absolute path it can chroot to.
		abs, err := filepath.Abs(cfg.rootfs)
		if err != nil {
			return fmt.Errorf("rootfs %q: %w", cfg.rootfs, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("rootfs %s: %w", abs, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("rootfs %s: not a directory", abs)
		}
		cfg.rootfs = abs
	}

	cgPath, err := createCgroup(fmt.Sprintf("cfs-%d", os.Getpid()), cfg)
	if err != nil {
		return err
	}
	// By the time cmd.Run returns the container is gone, so the cgroup is empty
	// and rmdir will succeed. Without this every run leaks a directory.
	defer removeCgroup(cgPath)

	fmt.Printf("run: pid %d, starting %v\n", os.Getpid(), cmdArgs)

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, cmdArgs...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(),
		envRootfs+"="+cfg.rootfs,
		envHostname+"="+cfg.hostname,
		envCgroup+"="+cgPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// NEWUTS: its own hostname. NEWPID: it becomes pid 1 in its own tree.
		// NEWNS: its own mount table, so mounting /proc can't touch the host.
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS,
		// Unsharing the mount namespace as well makes Go mark our root mount
		// MS_PRIVATE, which stops mounts from propagating back to the host.
		Unshareflags: syscall.CLONE_NEWNS,
	}

	return cmd.Run()
}

// child is the far side of the re-exec: it is already inside the namespaces, so
// this is where the process gets told the lies that make it a container.
func child(args []string) error {
	if len(args) == 0 {
		return errors.New("child: no command given")
	}
	fmt.Printf("child: pid %d, running %v\n", os.Getpid(), args)

	if cg := os.Getenv(envCgroup); cg != "" {
		// We are pid 1 here, and the kernel resolves the pid written to
		// cgroup.procs in the writer's PID namespace — so "1" really does mean
		// this process. Everything we fork afterwards inherits the cgroup.
		procs := filepath.Join(cg, "cgroup.procs")
		if err := os.WriteFile(procs, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
			return fmt.Errorf("join cgroup: %w", err)
		}
	}

	if hostname := os.Getenv(envHostname); hostname != "" {
		if err := syscall.Sethostname([]byte(hostname)); err != nil {
			return fmt.Errorf("sethostname %q: %w", hostname, err)
		}
	}

	if rootfs := os.Getenv(envRootfs); rootfs != "" {
		if err := syscall.Chroot(rootfs); err != nil {
			return fmt.Errorf("chroot %s: %w", rootfs, err)
		}
		if err := syscall.Chdir("/"); err != nil {
			return fmt.Errorf("chdir /: %w", err)
		}
	}

	// A fresh procfs so `ps` reports the container's PID namespace instead of
	// the host's. It lives in our private mount namespace and dies with us; the
	// unmount below is tidiness, not cleanup.
	if err := os.MkdirAll("/proc", 0555); err != nil {
		return fmt.Errorf("mkdir /proc: %w", err)
	}
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		return fmt.Errorf("mount /proc: %w", err)
	}
	defer func() {
		if err := syscall.Unmount("/proc", 0); err != nil {
			fmt.Fprintf(os.Stderr, "cfs: unmount /proc: %v\n", err)
		}
	}()

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = containerEnv()

	return cmd.Run()
}

// createCgroup makes a leaf cgroup for one container and writes its limits into
// it, returning the path so the caller can clean it up.
func createCgroup(name string, cfg config) (string, error) {
	// cgroup v2 is the only hierarchy that exposes cgroup.controllers at its
	// root; if that file is missing we are looking at v1 or at nothing.
	if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
		return "", fmt.Errorf("cgroup v2 not mounted at %s: %w", cgroupRoot, err)
	}

	// A controller only exists in a child cgroup if the parent delegates it.
	// This is usually already done by systemd, and re-enabling is a no-op, so a
	// failure here only matters if the limit file below never shows up.
	subtree := filepath.Join(cgroupRoot, "cgroup.subtree_control")
	_ = os.WriteFile(subtree, []byte("+pids +memory"), 0644)

	path := filepath.Join(cgroupRoot, name)
	if err := os.Mkdir(path, 0755); err != nil {
		return "", fmt.Errorf("create cgroup %s: %w", path, err)
	}

	limits := []struct{ file, value string }{
		{"pids.max", cfg.pids},
		{"memory.max", cfg.memory},
	}
	for _, l := range limits {
		if l.value == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(path, l.file), []byte(l.value), 0644); err != nil {
			removeCgroup(path)
			return "", fmt.Errorf("set %s=%s: %w", l.file, l.value, err)
		}
	}
	return path, nil
}

// removeCgroup rmdirs a cgroup. It only fails if something is still in it, and
// that is worth saying out loud rather than swallowing.
func removeCgroup(path string) {
	if err := os.Remove(path); err != nil {
		fmt.Fprintf(os.Stderr, "cfs: leaked cgroup %s: %v\n", path, err)
	}
}

// containerEnv is the environment minus our own plumbing, so the contained
// command doesn't see how it got here.
func containerEnv() []string {
	env := os.Environ()
	out := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "CFS_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
