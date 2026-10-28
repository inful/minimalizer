//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The loader passes an rpath on to the libraries of a binary, a runpath it
// does not. The tool has to keep the two apart, or it stops warning about
// the libraries that the image can only find through the loader cache.
//
// The Go linker writes a runpath, so a helper binary built with it has one.
// A C library built with --disable-new-dtags has an rpath.

func TestReadELFPutsTheRunPathApart(t *testing.T) {
	bin := buildHelper(t)
	info, err := readELF(bin)
	if err != nil {
		t.Fatalf("readELF() error = %v", err)
	}
	if len(info.runpath) != 1 || info.runpath[0] != "/opt/lib" {
		t.Errorf("runpath = %v, want [/opt/lib]", info.runpath)
	}
	if len(info.rpath) != 0 {
		t.Errorf("rpath = %v, want none", info.rpath)
	}
}

func TestReadELFPutsTheRPathApart(t *testing.T) {
	bin := buildCLibrary(t)
	info, err := readELF(bin)
	if err != nil {
		t.Fatalf("readELF() error = %v", err)
	}
	if len(info.rpath) != 1 || info.rpath[0] != "/opt/lib" {
		t.Errorf("rpath = %v, want [/opt/lib]", info.rpath)
	}
	if len(info.runpath) != 0 {
		t.Errorf("runpath = %v, want none", info.runpath)
	}
}

// buildHelper builds a small program with a runpath and returns the binary.
func buildHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "helper")
	// A static PIE has a dynamic section, so the linker writes the search
	// path into it.
	cmd := exec.Command("go", "build", "-buildmode=pie", "-ldflags", "-r /opt/lib", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Not a skip: a guard that steps aside without a word is worse
		// than no guard at all.
		t.Fatalf("cannot build a helper binary: %v\n%s", err, out)
	}
	return bin
}

// buildCLibrary builds a shared library with an rpath and returns it.
func buildCLibrary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "lib.c")
	if err := os.WriteFile(src, []byte("int f(void) { return 1; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "libtest.so")
	cmd := exec.Command("gcc", "-shared", "-fPIC", "-Wl,-rpath,/opt/lib", "-Wl,--disable-new-dtags", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("no C compiler here: %v\n%s", err, out)
	}
	return bin
}
