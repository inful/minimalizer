package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// lddDeps asks which shared objects a binary needs.
//
// The ELF interpreter of the binary is run with --list, which is what ldd(1)
// does under the hood, so the ldd command does not have to exist in the image.
func lddDeps(bin string, info elfInfo) ([]dep, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("minimalizer only runs on Linux")
	}
	if len(info.needed) == 0 {
		// The binary asks for nothing, so it is static and has no
		// libraries. There is no need to start the loader.
		return nil, nil
	}
	interp := info.interp
	if interp == "" {
		// A shared library has no interpreter of its own, so the system
		// loader has to list its needs.
		var err error
		interp, err = defaultLoader()
		if err != nil {
			return nil, err
		}
	}

	// The loader treats a name without a slash as the name of a library, so
	// it always gets an absolute path.
	abs, err := filepath.Abs(bin)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", bin, err)
	}
	cmd := exec.Command(interp, "--list", abs)
	cmd.Env = loaderEnv(os.Environ())
	// The loader prints the dependency list on stdout and its complaints
	// on stderr, so the two are kept apart.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	if name := missingLibrary(stdout.String(), stderr.String()); name != "" {
		return nil, fmt.Errorf("shared library %q not found, is it installed in this stage?", name)
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			if errors.Is(runErr, fs.ErrNotExist) {
				return nil, fmt.Errorf("the ELF interpreter %s does not exist in this stage", interp)
			}
			return nil, fmt.Errorf("running %s: %w", interp, runErr)
		}
		// The loader could not load the binary. Its list of libraries
		// would be short, so it is not used.
		return nil, fmt.Errorf("%s --list failed: %w\n%s", interp, runErr, strings.TrimSpace(stderr.String()))
	}
	deps, err := parseLdd(stdout.String())
	if err != nil {
		return nil, fmt.Errorf("%s --list output:\n%s\n%w", interp, strings.TrimSpace(stdout.String()), err)
	}
	// the kernel loads the interpreter by absolute path, so it has to be
	// part of the image even when the loader does not list it.
	if !slices.ContainsFunc(deps, func(d dep) bool { return d.path == interp }) {
		deps = append(deps, dep{path: interp})
	}
	return deps, nil
}

// defaultLoader finds the system loader, used for shared libraries.
func defaultLoader() (string, error) {
	for _, pattern := range []string{"/lib64/ld-*.so.*", "/lib/ld-*.so.*", "/usr/lib64/ld-*.so.*", "/usr/lib/ld-*.so.*"} {
		if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
			return matches[0], nil
		}
	}
	return "", fmt.Errorf("cannot find the system loader")
}

// loaderEnv drops the variables that would make the loader behave
// differently than it does in the image.
func loaderEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "LD_") {
			continue
		}
		out = append(out, e)
	}
	return out
}
