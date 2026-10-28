package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadELF(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the test binary is not an ELF file on this system")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	info, err := readELF(exe)
	if err != nil {
		t.Fatalf("readELF(%s) error = %v", exe, err)
	}
	// The test binary is a Go program: it is either static or a static
	// position independent binary, so it asks for no libraries at all.
	if len(info.needed) != 0 {
		t.Errorf("the test binary asks for %v, want nothing", info.needed)
	}
}

func TestReadELFLibrary(t *testing.T) {
	loader, err := defaultLoader()
	if err != nil {
		t.Skip("no system loader on this system")
	}
	info, err := readELF(loader)
	if err != nil {
		t.Fatalf("readELF(%s) error = %v", loader, err)
	}
	if info.interp != "" {
		t.Errorf("the loader has an interpreter of its own: %q", info.interp)
	}
}

func TestReadELFRefusesAnythingButELF(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"script.sh": "#!/bin/sh\necho hi\n",
		"short.sh":  "#!/bin/sh\n",
		"text.txt":  "this is not a binary at all, it is just some text\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := readELF(path); err == nil || !strings.Contains(err.Error(), "not an ELF binary") {
			t.Errorf("readELF(%s) error = %v, want not an ELF binary", name, err)
		}
	}
}
