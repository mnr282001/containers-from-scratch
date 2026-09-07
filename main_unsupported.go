//go:build !linux

// This file exists so the module still builds, vets and tests on a non-Linux
// machine instead of failing with "undefined: syscall.CLONE_NEWUTS". The real
// program is in main.go, behind a linux build tag.
package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	fmt.Fprintf(os.Stderr, `cfs only runs on Linux (this is %s/%s).

Namespaces, chroot and cgroups are Linux kernel features — there is nothing
here to build a container out of. Run it on a Linux box, VM, or container:

    docker run --rm -it --privileged -v "$PWD":/src -w /src golang:1 bash

The Linux code still type-checks from here:

    GOOS=linux go build ./...
`, runtime.GOOS, runtime.GOARCH)
	os.Exit(1)
}
