package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeELF writes a file that starts with the ELF magic, so the tool treats
// it as a binary.
func writeELF(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("\x7fELF\x02\x01\x01"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want options
	}{
		{
			name: "just a binary",
			args: []string{"/out/app"},
			want: options{stage: "builder", binaries: []string{"/out/app"}},
		},
		{
			name: "a stage name",
			args: []string{"--stage", "build", "/out/app"},
			want: options{stage: "build", binaries: []string{"/out/app"}},
		},
		{
			name: "package without a folder",
			args: []string{"--package", "/out/app"},
			want: options{stage: "builder", packageDir: defaultRootfs, binaries: []string{"/out/app"}},
		},
		{
			name: "package with a folder",
			args: []string{"--package=/out/rootfs", "/out/app"},
			want: options{stage: "builder", packageDir: "/out/rootfs", binaries: []string{"/out/app"}},
		},
		{
			name: "package turned off",
			args: []string{"--package=false", "/out/app"},
			want: options{stage: "builder", binaries: []string{"/out/app"}},
		},
		{
			name: "several binaries",
			args: []string{"/out/app", "/out/worker"},
			want: options{stage: "builder", binaries: []string{"/out/app", "/out/worker"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseArgs(tc.args)
			if err != nil {
				t.Fatalf("parseArgs() error = %v", err)
			}
			if opts.stage != tc.want.stage {
				t.Errorf("stage = %q, want %q", opts.stage, tc.want.stage)
			}
			if opts.packageDir != tc.want.packageDir {
				t.Errorf("packageDir = %q, want %q", opts.packageDir, tc.want.packageDir)
			}
			if strings.Join(opts.binaries, " ") != strings.Join(tc.want.binaries, " ") {
				t.Errorf("binaries = %v, want %v", opts.binaries, tc.want.binaries)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	bin := writeELF(t, filepath.Join(dir, "app"))

	tests := []struct {
		name string
		opts options
		want string
	}{
		{
			name: "a plain run",
			opts: options{stage: "builder", binaries: []string{bin}},
		},
		{
			name: "json and package together",
			opts: options{stage: "builder", asJSON: true, packageDir: defaultRootfs, binaries: []string{bin}},
			want: "cannot be combined",
		},
		{
			name: "an empty stage name",
			opts: options{stage: "", binaries: []string{bin}},
			want: "stage name cannot be empty",
		},
		{
			name: "a stage name with a slash",
			opts: options{stage: "a/b", binaries: []string{bin}},
			want: "cannot contain a slash",
		},
		{
			name: "a stage name with a space",
			opts: options{stage: "a b", binaries: []string{bin}},
			want: "Dockerfile line",
		},
		{
			name: "a stage name with a colon",
			opts: options{stage: "my:stage", binaries: []string{bin}},
			want: "not a Docker stage name",
		},
		{
			name: "a package folder with a space",
			opts: options{stage: "builder", packageDir: "/out/my tree", binaries: []string{bin}},
			want: "Dockerfile line",
		},
		{
			name: "a package folder with a newline",
			opts: options{stage: "builder", packageDir: "/out/tree\nRUN evil", binaries: []string{bin}},
			want: "Dockerfile line",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			err := validate(&opts)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validate() error = %v, want none", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate() did not fail, want an error about %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("validate() error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestCheckPackageForm(t *testing.T) {
	dir := t.TempDir()
	bin := writeELF(t, filepath.Join(dir, "app"))
	other := writeELF(t, filepath.Join(dir, "worker"))
	folder := filepath.Join(dir, "rootfs")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}

	// the old form: --package FOLDER BINARY. The folder exists.
	if err := checkPackageForm(defaultRootfs, []string{folder, bin}); err == nil {
		t.Error("checkPackageForm() did not catch an existing folder")
	}
	// the old form: the folder does not exist yet, the tool would create it.
	if err := checkPackageForm(defaultRootfs, []string{filepath.Join(dir, "new"), bin}); err == nil {
		t.Error("checkPackageForm() did not catch a folder that does not exist yet")
	}
	// the new form: one binary.
	if err := checkPackageForm(defaultRootfs, []string{bin}); err != nil {
		t.Errorf("checkPackageForm() error = %v, want none", err)
	}
	// the new form: several binaries.
	if err := checkPackageForm(defaultRootfs, []string{bin, other}); err != nil {
		t.Errorf("checkPackageForm() error = %v, want none", err)
	}
	// the new form: the first binary is not an ELF file. The error about
	// the file itself is better than advice about the flag.
	script := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkPackageForm(defaultRootfs, []string{script, bin}); err != nil {
		t.Errorf("checkPackageForm() error = %v, want none", err)
	}
}

func TestIsELF(t *testing.T) {
	dir := t.TempDir()
	bin := writeELF(t, filepath.Join(dir, "app"))
	if !isELF(bin) {
		t.Errorf("isELF(%q) = false, want true", bin)
	}
	other := filepath.Join(dir, "notelf")
	if err := os.WriteFile(other, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isELF(other) {
		t.Errorf("isELF(%q) = true, want false", other)
	}
	if isELF(filepath.Join(dir, "nowhere")) {
		t.Error("isELF() of a file that does not exist = true, want false")
	}
}
