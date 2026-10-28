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

func main() {
	stage := flag.String("stage", "builder", "name of the source stage used in COPY --from")
	ldCache := flag.Bool("ld-cache", false, "also copy /etc/ld.so.cache (needed when libraries live outside the loader's default search path)")
	asJSON := flag.Bool("json", false, "print JSON instead of Dockerfile statements")
	pkgDir := flag.String("package", "", "copy the needed files into this directory, laid out as the image root, and print the statements that move them there")
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
	if *asJSON && *pkgDir != "" {
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
	if err := checkWord("package folder", *pkgDir); err != nil {
		fmt.Fprintln(os.Stderr, "minimalizer:", err)
		os.Exit(2)
	}

	libs, err := collect(flag.Args(), *ldCache)
	if err != nil {
		fmt.Fprintln(os.Stderr, "minimalizer:", err)
		os.Exit(1)
	}

	switch {
	case *pkgDir != "":
		var dir string
		dir, err = packageLibs(libs, *pkgDir)
		if err == nil {
			err = writePackage(os.Stdout, libs, dir, *stage)
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
