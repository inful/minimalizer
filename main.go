// Command minimalizer reports the shared libraries a binary depends on and
// prints COPY --from statements that bring them into a scratch based image.
//
// The statements are safe for kaniko: every library is resolved to the real
// file behind its symlinks and copied to the path the dynamic loader expects,
// so the resulting image needs no symlinks at all.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// version is overridden at build time via -ldflags.
var version = "dev"

// defaultRootfs is where the image root is put when --package is given
// without a folder. It is a fixed name so the path shows up once in the
// Dockerfile: the RUN that fills it, and the COPY that moves it.
const defaultRootfs = "/rootfs"

// packageFlag is the --package flag. It takes no value: the tree goes to
// defaultRootfs. Write --package=DIR to name another folder.
type packageFlag struct {
	dir string
	set bool
}

func (p *packageFlag) String() string { return p.dir }

func (p *packageFlag) Set(v string) error {
	switch v {
	case "true":
		p.set = true
	case "false":
		p.set = false
		p.dir = ""
	default:
		p.set = true
		p.dir = v
	}
	return nil
}

// IsBoolFlag lets --package be used without a value.
func (p *packageFlag) IsBoolFlag() bool { return true }

func main() {
	stage := flag.String("stage", "builder", "name of the source stage used in COPY --from")
	ldCache := flag.Bool("ld-cache", false, "also copy /etc/ld.so.cache (needed when libraries live outside the loader's default search path)")
	asJSON := flag.Bool("json", false, "print JSON instead of Dockerfile statements")
	pkg := &packageFlag{}
	flag.Var(pkg, "package", "copy the binaries and their libraries into "+defaultRootfs+", laid out as the image root (write --package=DIR for another folder)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: minimalizer [flags] <binary> [binary...]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Printf("minimalizer %s\n", version)
		return
	}

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if pkg.set {
		// The old form, --package FOLDER BINARY, is easy to type by
		// mistake now that the flag takes no value.
		if fi, err := os.Stat(flag.Arg(0)); err == nil && fi.IsDir() {
			fmt.Fprintf(os.Stderr, "minimalizer: %s is a folder. The tree goes to %s; write --package=DIR to name another folder.\n", flag.Arg(0), defaultRootfs)
			os.Exit(2)
		}
	}
	if *asJSON && pkg.set {
		fmt.Fprintln(os.Stderr, "minimalizer: --json and --package cannot be combined")
		os.Exit(2)
	}
	if *stage == "" {
		fmt.Fprintln(os.Stderr, "minimalizer: the stage name cannot be empty")
		os.Exit(2)
	}
	if strings.Contains(*stage, "/") {
		fmt.Fprintln(os.Stderr, "minimalizer: the stage name cannot contain a slash")
		os.Exit(2)
	}
	if err := checkWord("stage name", *stage); err != nil {
		fmt.Fprintln(os.Stderr, "minimalizer:", err)
		os.Exit(2)
	}

	libs, err := collect(flag.Args(), *ldCache)
	if err != nil {
		fmt.Fprintln(os.Stderr, "minimalizer:", err)
		os.Exit(1)
	}

	switch {
	case pkg.set:
		dir := pkg.dir
		if dir == "" {
			dir = defaultRootfs
		}
		var abs string
		abs, err = packageLibs(libs, flag.Args(), dir)
		if err == nil {
			err = writePackage(os.Stdout, libs, flag.Args(), abs, *stage)
		}
	case *asJSON:
		err = writeJSON(os.Stdout, flag.Args(), libs, *stage)
	default:
		err = writeDockerfile(os.Stdout, libs, *stage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "minimalizer:", err)
		os.Exit(1)
	}
}
