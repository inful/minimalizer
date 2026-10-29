// Command minimalizer reports the shared libraries a binary depends on and
// prints COPY --from statements that bring them into a scratch based image.
//
// The statements are safe for kaniko: every library is resolved to the real
// file behind its symlinks and copied to the path the dynamic loader expects,
// so the resulting image needs no symlinks at all.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// version is overridden at build time via -ldflags.
var version = "dev"

// defaultRootfs is where the image root is put when --package is given
// without a folder. It is a fixed name so the path shows up once in the
// Dockerfile: the RUN that fills it, and the COPY that moves it.
const defaultRootfs = "/rootfs"

// reStageName is what Docker accepts as the name of a stage.
var reStageName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

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

// options is what the command line asks for.
type options struct {
	fs          *flag.FlagSet
	stage       string
	ldCache     bool
	asJSON      bool
	packageDir  string // empty means: do not package
	binaries    []string
	showVersion bool
}

// parseArgs reads the command line. It always returns the options it could
// read, so the caller can print the usage next to an error.
func parseArgs(args []string) (*options, error) {
	opts := &options{}
	fs := flag.NewFlagSet("minimalizer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts.fs = fs

	stage := fs.String("stage", "builder", "name of the source stage used in COPY --from")
	ldCache := fs.Bool("ld-cache", false, "also copy /etc/ld.so.cache (needed when libraries live outside the loader's default search path)")
	asJSON := fs.Bool("json", false, "print JSON instead of Dockerfile statements")
	pkg := &packageFlag{}
	fs.Var(pkg, "package", "copy the binaries and their libraries into "+defaultRootfs+", laid out as the image root (write --package=DIR for another folder)")
	showVersion := fs.Bool("version", false, "print the version and exit")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	opts.stage = *stage
	opts.ldCache = *ldCache
	opts.asJSON = *asJSON
	opts.showVersion = *showVersion
	opts.binaries = fs.Args()
	if pkg.set {
		opts.packageDir = pkg.dir
		if opts.packageDir == "" {
			opts.packageDir = defaultRootfs
		}
	}
	return opts, nil
}

// validate refuses the combinations the tool cannot act on. Every refusal
// is a usage error.
func validate(opts *options) error {
	if opts.asJSON && opts.packageDir != "" {
		return errors.New("--json and --package cannot be combined")
	}
	if opts.stage == "" {
		return errors.New("the stage name cannot be empty")
	}
	if strings.Contains(opts.stage, "/") {
		return errors.New("the stage name cannot contain a slash")
	}
	if !reStageName.MatchString(opts.stage) {
		// A name Docker cannot use as a stage, for example one with a
		// colon: Docker would read it as the name of an image. The rule
		// is stricter than the one for a Dockerfile line, so it answers
		// for every character a stage name may not hold.
		return fmt.Errorf("the stage name %q is not a Docker stage name (letters, digits, _ . and - only)", opts.stage)
	}
	if opts.packageDir != "" {
		// The folder goes into a COPY statement, so it must not be able
		// to break that line.
		abs, err := filepath.Abs(opts.packageDir)
		if err != nil {
			return fmt.Errorf("cannot resolve %s: %w", opts.packageDir, err)
		}
		if err := checkWord("package folder", abs); err != nil {
			return err
		}
		opts.packageDir = abs
		if err := checkPackageForm(opts.packageDir, opts.binaries); err != nil {
			return err
		}
	}
	return nil
}

// checkPackageForm catches the old "--package FOLDER BINARY" form. The flag
// takes no value now, so the folder ends up where the first binary should
// be. The folder usually does not exist yet, because the tool is the one
// that creates it, so it is recognised by the first argument not being a
// binary while a later one is.
func checkPackageForm(dir string, binaries []string) error {
	if len(binaries) < 2 {
		return nil
	}
	first := binaries[0]
	if fi, err := os.Stat(first); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("%s is a folder. The tree goes to %s; write --package=DIR to name another folder", first, dir)
		}
		// The file is there. If it is not a binary, the error about the
		// file itself is better than advice about the flag.
		return nil
	}
	// The first argument does not exist. That is what the old form looks
	// like: the folder is the one the tool is going to create.
	for _, b := range binaries[1:] {
		if isELF(b) {
			return fmt.Errorf("%s does not exist. The tree goes to %s; write --package=DIR to name another folder", first, dir)
		}
	}
	return nil
}

// globChars are the characters that make an argument a pattern.
const globChars = "*?["

// expandBinaries turns the arguments into the list of binaries to analyse.
// An argument with a glob character in it is a pattern: every binary it
// matches is analysed. A file that is not a binary and a folder are skipped,
// so a pattern can point at a folder of a distributor.
//
// A pattern that matches nothing is an error: a typo must not leave the
// image without the binaries.
func expandBinaries(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if !strings.ContainsAny(arg, globChars) {
			out = append(out, arg)
			continue
		}
		if strings.Contains(arg, "**") {
			// The glob of the standard library matches one level of
			// folders. A ** would match fewer folders than the user
			// thinks, and the missing binaries would only show up in the
			// image.
			return nil, fmt.Errorf("%q: ** is not supported, a pattern matches one level of folders", arg)
		}
		matches, err := filepath.Glob(arg)
		if err != nil {
			return nil, fmt.Errorf("%q is not a valid pattern: %w", arg, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no file matches %q", arg)
		}
		binaries := 0
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil {
				fmt.Fprintf(os.Stderr, "minimalizer: warning: %s: %v, it is skipped\n", m, err)
				continue
			}
			if !fi.Mode().IsRegular() {
				// a folder or something else: not a binary
				continue
			}
			if !isELF(m) {
				fmt.Fprintf(os.Stderr, "minimalizer: warning: %s is not a binary, it is skipped\n", m)
				continue
			}
			out = append(out, m)
			binaries++
		}
		if binaries == 0 {
			return nil, fmt.Errorf("no binary matches %q", arg)
		}
	}
	return unique(out), nil
}

// unique removes the duplicates, keeping the order.
func unique(list []string) []string {
	seen := make(map[string]bool, len(list))
	out := list[:0]
	for _, s := range list {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// isELF reports whether the file starts with the ELF magic.
func isELF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return string(magic[:]) == "\x7fELF"
}

// usage prints how the tool is called.
func usage(w io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(w, "usage: minimalizer [flags] <binary|pattern> [binary|pattern...]\n\n")
	fs.SetOutput(w)
	fs.PrintDefaults()
	_, _ = fmt.Fprintf(w, "\nAn argument with a * in it is a pattern: every binary it matches is analysed.\n")
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(os.Stdout, opts.fs)
			return
		}
		fmt.Fprintln(os.Stderr, "minimalizer:", err)
		usage(os.Stderr, opts.fs)
		os.Exit(2)
	}

	if opts.showVersion {
		fmt.Printf("minimalizer %s\n", version)
		return
	}

	if len(opts.binaries) == 0 {
		usage(os.Stderr, opts.fs)
		os.Exit(2)
	}
	// The arguments may be patterns: a distributor ships a folder of
	// binaries and nobody wants to list them one by one.
	opts.binaries, err = expandBinaries(opts.binaries)
	if err != nil {
		fmt.Fprintln(os.Stderr, "minimalizer:", err)
		os.Exit(2)
	}
	if err := validate(opts); err != nil {
		fmt.Fprintln(os.Stderr, "minimalizer:", err)
		os.Exit(2)
	}

	libs, err := collect(opts.binaries, opts.ldCache)
	if err != nil {
		fmt.Fprintln(os.Stderr, "minimalizer:", err)
		os.Exit(1)
	}

	switch {
	case opts.packageDir != "":
		var abs string
		abs, err = packageLibs(libs, opts.binaries, opts.packageDir)
		if err == nil {
			err = writePackage(os.Stdout, libs, opts.binaries, abs, opts.stage)
		}
	case opts.asJSON:
		err = writeJSON(os.Stdout, opts.binaries, libs, opts.stage)
	default:
		err = writeDockerfile(os.Stdout, libs, opts.stage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "minimalizer:", err)
		os.Exit(1)
	}
}
