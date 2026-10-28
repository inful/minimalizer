package main

import (
	"testing"
)

func TestParseLdd(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    []dep
		wantErr bool
	}{
		{
			name: "glibc",
			out: "\tlinux-vdso.so.1 (0x00007ffd8e5f9000)\n" +
				"\tlibm.so.6 => /lib/x86_64-linux-gnu/libm.so.6 (0x00007f2a4c000000)\n" +
				"\tlibc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x00007f2a4c400000)\n" +
				"\t/lib64/ld-linux-x86-64.so.2 (0x00007f2a4c4a5000)\n",
			want: []dep{
				{soname: "libm.so.6", path: "/lib/x86_64-linux-gnu/libm.so.6"},
				{soname: "libc.so.6", path: "/lib/x86_64-linux-gnu/libc.so.6"},
				{path: "/lib64/ld-linux-x86-64.so.2"},
			},
		},
		{
			name: "musl",
			out: "/lib/ld-musl-x86_64.so.1 (0x7f4c0a000000)\n" +
				"libc.musl-x86_64.so.1 => /lib/ld-musl-x86_64.so.1 (0x7f4c0a000000)\n" +
				"libgcc_s.so.1 => /usr/lib/libgcc_s.so.1 (0x7f4c0a1a3000)\n",
			want: []dep{
				{path: "/lib/ld-musl-x86_64.so.1"},
				{soname: "libc.musl-x86_64.so.1", path: "/lib/ld-musl-x86_64.so.1"},
				{soname: "libgcc_s.so.1", path: "/usr/lib/libgcc_s.so.1"},
			},
		},
		{
			name: "a library in a folder with a space",
			out:  "\tlibspaced.so => /opt/my libs/libspaced.so (0x00007f2a4c000000)\n",
			want: []dep{
				{soname: "libspaced.so", path: "/opt/my libs/libspaced.so"},
			},
		},
		{
			name:    "unparsable dependency line",
			out:     "\tsomething weird => ???\n",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLdd(tc.out)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseLdd() = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLdd() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseLdd() = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("dep %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestMissingLibrary(t *testing.T) {
	tests := []struct {
		stdout string
		stderr string
		want   string
	}{
		{stdout: "\tlibfoo.so.1 => not found\n", want: "libfoo.so.1"},
		{stderr: "/bin/ls: error while loading shared libraries: libselinux.so.1: cannot open shared object file: No such file or directory\n", want: "libselinux.so.1"},
		{stderr: "Error loading shared library libfoo.so: No such file or directory\n", want: "libfoo.so"},
		{stdout: "\tlibc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x00007f2a4c400000)\n"},
	}
	for _, tc := range tests {
		if got := missingLibrary(tc.stdout, tc.stderr); got != tc.want {
			t.Errorf("missingLibrary(%q, %q) = %q, want %q", tc.stdout, tc.stderr, got, tc.want)
		}
	}
}
