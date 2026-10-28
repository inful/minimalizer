package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "libc-2.36.so")
	if err := os.WriteFile(target, []byte("elf"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "libc.so.6")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	l, err := resolve(dep{soname: "libc.so.6", path: link})
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	// the temp dir itself may live behind a symlink, compare resolved paths.
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if l.src != want {
		t.Errorf("src = %q, want %q", l.src, want)
	}
	if l.dest != link {
		t.Errorf("dest = %q, want %q", l.dest, link)
	}
	if l.kind != kindLib {
		t.Errorf("kind = %q, want %q", l.kind, kindLib)
	}
}

func TestResolveMarksInterpreter(t *testing.T) {
	dir := t.TempDir()
	interp := filepath.Join(dir, "ld-linux-x86-64.so.2")
	if err := os.WriteFile(interp, []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}

	l, err := resolve(dep{path: interp})
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if l.kind != kindInterp {
		t.Errorf("kind = %q, want %q", l.kind, kindInterp)
	}
}

func TestResolveRejectsPathsThatBreakTheDockerfile(t *testing.T) {
	for _, path := range []string{"/lib/with space.so", "/lib/with#hash.so", "/lib/with$dollar.so", "/lib/with\"quote.so"} {
		if _, err := resolve(dep{path: path}); err == nil {
			t.Errorf("resolve(%q) did not fail", path)
		}
	}
}

func TestNeedsCache(t *testing.T) {
	tests := []struct {
		dest  string
		paths []string
		want  bool
	}{
		{dest: "/lib/x86_64-linux-gnu/libc.so.6", want: false},
		{dest: "/usr/lib/aarch64-linux-gnu/libc.so.6", want: false},
		{dest: "/lib64/ld-linux-x86-64.so.2", want: false},
		{dest: "/usr/local/lib/libfoo.so", want: false},
		{dest: "/opt/lib/libfoo.so", want: true},
		{dest: "/opt/lib/libfoo.so", paths: []string{"/opt/lib"}, want: false},
		{dest: "/go/pkg/lib/libfoo.so", paths: []string{"/go/pkg/lib"}, want: false},
	}
	for _, tc := range tests {
		if got := needsCache(tc.dest, tc.paths); got != tc.want {
			t.Errorf("needsCache(%q, %v) = %v, want %v", tc.dest, tc.paths, got, tc.want)
		}
	}
}

func TestMerge(t *testing.T) {
	dir := t.TempDir()
	libc := filepath.Join(dir, "libc.so.6")
	if err := os.WriteFile(libc, []byte("elf"), 0o644); err != nil {
		t.Fatal(err)
	}
	libm := filepath.Join(dir, "libm.so.6")
	if err := os.WriteFile(libm, []byte("elf"), 0o644); err != nil {
		t.Fatal(err)
	}

	byDest := map[string]lib{}
	deps := []dep{
		{soname: "libc.so.6", path: libc},
		{soname: "libm.so.6", path: libm},
	}
	if err := merge(byDest, deps, "/app"); err != nil {
		t.Fatalf("merge() error = %v", err)
	}
	if len(byDest) != 2 {
		t.Fatalf("merge() kept %d files, want 2", len(byDest))
	}

	// the same library from a second binary keeps the first source and
	// remembers both binaries.
	if err := merge(byDest, deps[:1], "/other"); err != nil {
		t.Fatalf("merge() error = %v", err)
	}
	l := byDest[libc]
	want, err := filepath.EvalSymlinks(libc)
	if err != nil {
		t.Fatal(err)
	}
	if l.src != want {
		t.Errorf("src = %q, want %q", l.src, want)
	}
	if len(l.usedBy) != 2 || l.usedBy[0] != "/app" || l.usedBy[1] != "/other" {
		t.Errorf("usedBy = %v, want [/app /other]", l.usedBy)
	}

	// a library that cannot be resolved is an error.
	err = merge(map[string]lib{}, []dep{{soname: "libfoo.so.1", path: "/nowhere/libfoo.so.1"}}, "/app")
	if err == nil || !strings.Contains(err.Error(), "libfoo.so.1") {
		t.Errorf("merge() error = %v, want a missing library error", err)
	}
}

func TestPackageLibs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "libc-2.36.so")
	if err := os.WriteFile(src, []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	libs := []lib{{
		kind:   kindLib,
		soname: "libc.so.6",
		dest:   "/lib/x86_64-linux-gnu/libc.so.6",
		src:    src,
	}}

	pkg := filepath.Join(dir, "rootfs")
	abs, err := packageLibs(libs, pkg)
	if err != nil {
		t.Fatalf("packageLibs() error = %v", err)
	}
	if abs != pkg {
		t.Errorf("dir = %q, want %q", abs, pkg)
	}

	dst := filepath.Join(pkg, "lib/x86_64-linux-gnu/libc.so.6")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading %s: %v", dst, err)
	}
	if string(got) != "elf" {
		t.Errorf("content = %q, want %q", got, "elf")
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want -rwxr-xr-x", fi.Mode().Perm())
	}

	// a file from an earlier run does not survive.
	stale := filepath.Join(pkg, "lib", "stale.so")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := packageLibs(libs, pkg); err != nil {
		t.Fatalf("packageLibs() error = %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("%s survived the second run", stale)
	}

	// a folder minimalizer did not make is never touched.
	foreign := filepath.Join(dir, "foreign")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(foreign, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := packageLibs(libs, foreign); err == nil {
		t.Errorf("packageLibs() wrote into a folder it did not make")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the file in the foreign folder was touched: %v", err)
	}

	// a folder that exists and is empty is used.
	empty := filepath.Join(dir, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := packageLibs(libs, empty); err != nil {
		t.Errorf("packageLibs() refused an empty folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(empty, "lib/x86_64-linux-gnu/libc.so.6")); err != nil {
		t.Errorf("the library was not copied into the empty folder: %v", err)
	}
}

func TestPackageLibsMakesNoFolderForAStaticBinary(t *testing.T) {
	// A static binary has no libraries, so there is nothing to copy: no
	// folder, and no COPY statement either.
	dir := t.TempDir()
	pkg := filepath.Join(dir, "rootfs")
	abs, err := packageLibs(nil, pkg)
	if err != nil {
		t.Fatalf("packageLibs() error = %v", err)
	}
	if abs != pkg {
		t.Errorf("dir = %q, want %q", abs, pkg)
	}
	if _, err := os.Stat(pkg); !os.IsNotExist(err) {
		t.Errorf("%s was made, want no folder", pkg)
	}

	var sb strings.Builder
	if err := writePackage(&sb, nil, pkg, "builder"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sb.String(), "COPY") {
		t.Errorf("output %q copies something, want nothing", sb.String())
	}
}

func TestCopyFileOntoItself(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "libc.so.6")
	if err := os.WriteFile(path, []byte("important"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(path, path); err != nil {
		t.Fatalf("copyFile() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "important" {
		t.Errorf("content = %q, want %q", got, "important")
	}
}

func TestCheckWord(t *testing.T) {
	if err := checkWord("stage name", "builder"); err != nil {
		t.Errorf("checkWord() error = %v", err)
	}
	if err := checkWord("stage name", ""); err != nil {
		t.Errorf("checkWord() error = %v", err)
	}
	for _, word := range []string{"my builder", "a#b", "a$b", "a\"b", "--json"} {
		if err := checkWord("stage name", word); err == nil {
			t.Errorf("checkWord(%q) did not fail", word)
		}
	}
}

func TestWriteDockerfile(t *testing.T) {
	libs := []lib{{kind: kindLib, soname: "libc.so.6", dest: "/lib/libc.so.6", src: "/usr/lib/libc-2.36.so"}}
	var sb strings.Builder
	if err := writeDockerfile(&sb, libs, "builder"); err != nil {
		t.Fatal(err)
	}
	want := "COPY --from=builder /usr/lib/libc-2.36.so /lib/libc.so.6\n"
	if !strings.Contains(sb.String(), want) {
		t.Errorf("output %q does not contain %q", sb.String(), want)
	}
}

func TestWritePackage(t *testing.T) {
	libs := []lib{
		{kind: kindLib, soname: "libc.so.6", dest: "/lib/x86_64-linux-gnu/libc.so.6", src: "/usr/lib/libc-2.36.so"},
		{kind: kindInterp, dest: "/lib64/ld-linux-x86-64.so.2", src: "/usr/lib/ld-linux-x86-64.so.2"},
	}
	var sb strings.Builder
	if err := writePackage(&sb, libs, "/out/rootfs", "builder"); err != nil {
		t.Fatal(err)
	}
	want := "COPY --from=builder /out/rootfs/lib/ /lib/\n" +
		"COPY --from=builder /out/rootfs/lib64/ /lib64/\n"
	if !strings.Contains(sb.String(), want) {
		t.Errorf("output %q does not contain %q", sb.String(), want)
	}
	// the marker file must not end up in the image
	if strings.Contains(sb.String(), markerName) {
		t.Errorf("output %q copies the marker file", sb.String())
	}
}

func TestTopFolders(t *testing.T) {
	libs := []lib{
		{dest: "/lib/x86_64-linux-gnu/libc.so.6"},
		{dest: "/lib/x86_64-linux-gnu/libm.so.6"},
		{dest: "/lib64/ld-linux-x86-64.so.2"},
		{dest: "/libc.so.6"},
	}
	got := topFolders(libs)
	if len(got) != 3 {
		t.Fatalf("topFolders() = %v, want three entries", got)
	}
	if got[0].name != "lib" || !got[0].isDir {
		t.Errorf("topFolders()[0] = %v, want the folder lib", got[0])
	}
	if got[1].name != "lib64" || !got[1].isDir {
		t.Errorf("topFolders()[1] = %v, want the folder lib64", got[1])
	}
	if got[2].name != "libc.so.6" || got[2].isDir {
		t.Errorf("topFolders()[2] = %v, want the file libc.so.6", got[2])
	}
}

func TestWriteJSON(t *testing.T) {
	libs := []lib{{
		kind:   kindLib,
		soname: "libc.so.6",
		dest:   "/lib/libc.so.6",
		src:    "/usr/lib/libc-2.36.so",
		size:   10,
		usedBy: []string{"/app"},
	}}
	var sb strings.Builder
	if err := writeJSON(&sb, []string{"/app"}, libs, "builder"); err != nil {
		t.Fatal(err)
	}
	var report jsonReport
	if err := json.Unmarshal([]byte(sb.String()), &report); err != nil {
		t.Fatalf("the output is not valid JSON: %v", err)
	}
	if len(report.Binaries) != 1 || report.Binaries[0] != "/app" {
		t.Errorf("binaries = %v, want [/app]", report.Binaries)
	}
	if len(report.Libraries) != 1 {
		t.Fatalf("libraries = %v, want one entry", report.Libraries)
	}
	l := report.Libraries[0]
	if l.Soname != "libc.so.6" || l.Path != "/lib/libc.so.6" || l.RealPath != "/usr/lib/libc-2.36.so" {
		t.Errorf("library = %+v, want the libc.so.6 entry", l)
	}
	if l.Copy != "COPY --from=builder /usr/lib/libc-2.36.so /lib/libc.so.6" {
		t.Errorf("copy = %q", l.Copy)
	}
}
