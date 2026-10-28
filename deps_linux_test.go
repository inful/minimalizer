//go:build linux

package main

import (
	"debug/elf"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultLoaderTakesTheMachineOfTheLibrary(t *testing.T) {
	loader, err := defaultLoader(elf.EM_NONE)
	if err != nil {
		t.Skip("no system loader on this system")
	}
	info, err := readELF(loader)
	if err != nil {
		t.Fatalf("readELF(%s) error = %v", loader, err)
	}
	// A stage with the libraries of more than one architecture installed
	// must still get the loader of the machine it is asked about.
	got, err := defaultLoader(info.machine)
	if err != nil {
		t.Fatalf("defaultLoader(%v) error = %v", info.machine, err)
	}
	if !isForMachine(got, info.machine) {
		t.Errorf("defaultLoader(%v) = %q, want a loader for that machine", info.machine, got)
	}
}

func TestLddDeps(t *testing.T) {
	// The test binary is a Go program. It is static without cgo and
	// dynamic with it (the race detector needs cgo), so both cases are
	// possible here.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	info, err := readELF(exe)
	if err != nil {
		t.Fatalf("readELF() error = %v", err)
	}
	deps, err := lddDeps(exe, info)
	if err != nil {
		t.Fatalf("lddDeps() error = %v", err)
	}
	if len(info.needed) == 0 {
		// A static binary needs nothing, and the loader is not started.
		if len(deps) != 0 {
			t.Errorf("lddDeps() = %v, want no libraries for a static binary", deps)
		}
		return
	}
	// A dynamic binary needs at least the interpreter, which the kernel
	// loads by absolute path.
	found := false
	for _, d := range deps {
		if d.path == info.interp {
			found = true
		}
	}
	if !found {
		t.Errorf("lddDeps() = %v, want the interpreter %q among them", deps, info.interp)
	}
}

func TestLddDepsOnADynamicBinary(t *testing.T) {
	// Find a binary of the system that needs libraries.
	for _, candidate := range []string{"/bin/ls", "/usr/bin/ls", "/bin/sh", "/usr/bin/sh"} {
		info, err := readELF(candidate)
		if err != nil || len(info.needed) == 0 {
			continue
		}
		deps, err := lddDeps(candidate, info)
		if err != nil {
			t.Fatalf("lddDeps(%s) error = %v", candidate, err)
		}
		if len(deps) == 0 {
			t.Errorf("lddDeps(%s) = no libraries, but it asks for %v", candidate, info.needed)
		}
		for _, d := range deps {
			if d.path == "" {
				t.Errorf("lddDeps(%s) = %q has no path", candidate, d.soname)
			}
		}
		return
	}
	t.Skip("no dynamic binary on this system")
}

func TestLddDepsWithARelativePath(t *testing.T) {
	// Find a binary of the system that needs libraries.
	binary := ""
	for _, candidate := range []string{"/bin/ls", "/usr/bin/ls", "/bin/sh", "/usr/bin/sh"} {
		info, err := readELF(candidate)
		if err == nil && len(info.needed) > 0 {
			binary = candidate
			break
		}
	}
	if binary == "" {
		t.Skip("no dynamic binary on this system")
	}

	// The loader takes a name without a slash for the name of a library,
	// so a bare name has to work all the same.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(binary)
	if err := os.Chdir(filepath.Dir(binary)); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(dir) }()

	info, err := readELF(name)
	if err != nil {
		t.Fatalf("readELF(%s) error = %v", name, err)
	}
	deps, err := lddDeps(name, info)
	if err != nil {
		t.Fatalf("lddDeps(%s) error = %v", name, err)
	}
	if len(deps) == 0 {
		t.Errorf("lddDeps(%s) = no libraries, but it asks for %v", name, info.needed)
	}
}

func TestCollect(t *testing.T) {
	binary := ""
	for _, candidate := range []string{"/bin/ls", "/usr/bin/ls", "/bin/sh", "/usr/bin/sh"} {
		info, err := readELF(candidate)
		if err == nil && len(info.needed) > 0 {
			binary = candidate
			break
		}
	}
	if binary == "" {
		t.Skip("no dynamic binary on this system")
	}

	libs, err := collect([]string{binary}, false)
	if err != nil {
		t.Fatalf("collect() error = %v", err)
	}
	if len(libs) == 0 {
		t.Fatal("collect() found no libraries")
	}
	// the interpreter comes first, the kernel needs it by absolute path
	if libs[0].kind != kindInterp {
		t.Errorf("the first library is %v, want the interpreter", libs[0])
	}
	for _, l := range libs {
		if l.dest == "" || l.src == "" {
			t.Errorf("library %v has no path", l)
		}
	}

	// with the cache, the cache is in the list as well
	if _, err := os.Stat("/etc/ld.so.cache"); err != nil {
		return
	}
	libs, err = collect([]string{binary}, true)
	if err != nil {
		t.Fatalf("collect() error = %v", err)
	}
	found := false
	for _, l := range libs {
		if l.kind == kindCache {
			found = true
		}
	}
	if !found {
		t.Errorf("collect() with the cache = %v, want the cache in the list", libs)
	}
}
