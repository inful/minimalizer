//go:build linux

package main

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLddDepsSaysTheInterpreterIsMissing(t *testing.T) {
	// The binary names an interpreter that this stage does not have.
	_, err := lddDeps("/bin/ls", elfInfo{interp: "/nonexistent/ld.so", needed: []string{"libc.so.6"}, machine: stageMachine()})
	if err == nil || !strings.Contains(err.Error(), "does not exist in this stage") {
		t.Errorf("lddDeps() error = %v, want it to say the interpreter is missing", err)
	}
}

func TestLddDepsSaysTheLibraryIsMissing(t *testing.T) {
	// A loader that reports a library it could not load.
	dir := t.TempDir()
	loader := filepath.Join(dir, "ld.so")
	if err := os.WriteFile(loader, []byte("#!/bin/sh\necho '\tlibfoo.so.1 => not found'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := lddDeps("/bin/ls", elfInfo{interp: loader, needed: []string{"libfoo.so.1"}, machine: stageMachine()})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("lddDeps() error = %v, want it to name the missing library", err)
	}
}

func TestLddDepsSaysTheBinaryIsOfAnotherMachine(t *testing.T) {
	// The loader of this stage cannot list the libraries of a binary
	// built for another machine.
	if stageMachine() == elf.EM_NONE {
		t.Skip("the machine of this host cannot be told")
	}
	_, err := lddDeps("/bin/ls", elfInfo{interp: "/lib64/ld-linux-x86-64.so.2", needed: []string{"libc.so.6"}, machine: otherMachine(t)})
	if err == nil || !strings.Contains(err.Error(), "this stage is") {
		t.Errorf("lddDeps() error = %v, want it to say the binary is of another machine", err)
	}
}

func TestDefaultLoaderHasNoLoaderForAnotherMachine(t *testing.T) {
	// A loader of another architecture would answer with a wrong list, so
	// the stage has to be reported as not having a loader at all. PowerPC
	// is used because no runner has a loader for it.
	if _, err := defaultLoader(elf.EM_PPC64); err == nil {
		t.Error("defaultLoader(EM_PPC64) found a loader, want none")
	}
}

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

func TestLddDepsOnAStaticPIE(t *testing.T) {
	// A static PIE asks for no library, but it names an interpreter: the
	// kernel loads that file by absolute path, so the image needs it.
	bin := buildStaticPIE(t)
	info, err := readELF(bin)
	if err != nil {
		t.Fatalf("readELF() error = %v", err)
	}
	if len(info.needed) != 0 {
		t.Skipf("the binary built here is not static: it asks for %v", info.needed)
	}
	if info.interp == "" {
		t.Skip("the binary built here names no interpreter")
	}

	deps, err := lddDeps(bin, info)
	if err != nil {
		t.Fatalf("lddDeps() error = %v", err)
	}
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

// buildStaticPIE builds a program that is a PIE but asks for no library.
func buildStaticPIE(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "staticpie")
	// CGO off, so the binary asks for no library. A PIE, so it still names
	// an interpreter.
	cmd := exec.Command("go", "build", "-buildmode=pie", "-o", bin, src)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cannot build a static PIE: %v\n%s", err, out)
	}
	return bin
}
