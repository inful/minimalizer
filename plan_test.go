package main

import (
	"encoding/json"
	"io"
	"io/fs"
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

func TestWriteDockerfileRejectsPathsThatBreakIt(t *testing.T) {
	for _, path := range []string{"/lib/with space.so", "/lib/with#hash.so", "/lib/with$dollar.so", "/lib/with\"quote.so", "/lib/with[bracket.so"} {
		libs := []lib{{kind: kindLib, soname: "libfoo.so", dest: path, src: path}}
		var sb strings.Builder
		if err := writeDockerfile(&sb, libs, "builder"); err == nil {
			t.Errorf("writeDockerfile() did not fail on %q", path)
		}
	}
}

func TestWriteDockerfileRejectsRealPathsThatBreakIt(t *testing.T) {
	// The real file behind the symlink is what the COPY statement names,
	// so it has to be printable in a Dockerfile line as well.
	dir := t.TempDir()
	target := filepath.Join(dir, "with space.so")
	if err := os.WriteFile(target, []byte("elf"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "libfoo.so.1")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	libs := []lib{{kind: kindLib, soname: "libfoo.so.1", dest: "/lib/libfoo.so.1", src: target}}
	var sb strings.Builder
	if err := writeDockerfile(&sb, libs, "builder"); err == nil {
		t.Error("writeDockerfile() did not fail on a real path with a space")
	}
}

func TestWritePackageAcceptsAPathWithASpace(t *testing.T) {
	// In the package mode the paths only go into comments, and the tree is
	// moved with one statement, so a space in a path is fine.
	libs := []lib{{kind: kindLib, soname: "libfoo.so", dest: "/opt/my libs/libfoo.so", src: "/opt/my libs/libfoo.so"}}
	var sb strings.Builder
	if err := writePackage(&sb, libs, nil, "/out/rootfs", "builder"); err != nil {
		t.Errorf("writePackage() error = %v, want none", err)
	}
	if !strings.Contains(sb.String(), "COPY --from=builder /out/rootfs/ /") {
		t.Errorf("output = %q, want the statement for the tree", sb.String())
	}
}

func TestWriteJSONRejectsPathsThatBreakTheCopyLine(t *testing.T) {
	for _, path := range []string{"/opt/my libs/libfoo.so", "/opt/lib[1]/libfoo.so"} {
		libs := []lib{{kind: kindLib, soname: "libfoo.so", dest: path, src: path}}
		var sb strings.Builder
		if err := writeJSON(&sb, []string{"/out/app"}, libs, "builder"); err == nil {
			t.Errorf("writeJSON() did not fail on %q", path)
		}
	}
}

func TestCheckCommentPath(t *testing.T) {
	// A newline would break out of the comment the path is written in, a
	// space would not.
	if err := checkCommentPath("/opt/lib\nname.so"); err == nil {
		t.Error("checkCommentPath() did not fail on a path with a newline")
	}
	if err := checkCommentPath("/opt/my libs/libfoo.so"); err != nil {
		t.Errorf("checkCommentPath() error = %v, want none", err)
	}
}

func TestNeedsCache(t *testing.T) {
	tests := []struct {
		name  string
		lib   lib
		paths searchPaths
		want  bool
	}{
		{
			name: "a library in a default folder",
			lib:  lib{soname: "libc.so.6", dest: "/lib/x86_64-linux-gnu/libc.so.6"},
		},
		{
			name: "a library in /usr/local/lib",
			lib:  lib{soname: "libfoo.so", dest: "/usr/local/lib/libfoo.so"},
		},
		{
			name: "a library outside every default folder",
			lib:  lib{soname: "libfoo.so", dest: "/opt/lib/libfoo.so"},
			want: true,
		},
		{
			name:  "a library under the rpath of the binary",
			lib:   lib{soname: "libfoo.so", dest: "/opt/lib/libfoo.so"},
			paths: searchPaths{rpath: []string{"/opt/lib"}},
		},
		{
			name:  "a library under the runpath of the binary",
			lib:   lib{soname: "libfoo.so", dest: "/opt/lib/libfoo.so"},
			paths: searchPaths{needed: []string{"libfoo.so"}, runpath: []string{"/opt/lib"}},
		},
		{
			// The loader does not pass a runpath on to the libraries of
			// a library, so this one is only found through the cache.
			name:  "a library of a library under the runpath of the binary",
			lib:   lib{soname: "libbar.so", dest: "/opt/lib/libbar.so"},
			paths: searchPaths{needed: []string{"libfoo.so"}, runpath: []string{"/opt/lib"}},
			want:  true,
		},
		{
			// An rpath is inherited, so this one is found.
			name:  "a library of a library under the rpath of the binary",
			lib:   lib{soname: "libbar.so", dest: "/opt/lib/libbar.so"},
			paths: searchPaths{needed: []string{"libfoo.so"}, rpath: []string{"/opt/lib"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsCache(tc.lib, tc.paths); got != tc.want {
				t.Errorf("needsCache(%q) = %v, want %v", tc.lib.dest, got, tc.want)
			}
		})
	}
}

func TestWarnNeedsCache(t *testing.T) {
	libs := []lib{{
		kind:   kindLib,
		soname: "libfoo.so",
		dest:   "/opt/lib/libfoo.so",
		usedBy: []string{"/out/app", "/out/worker"},
	}}
	// The first binary finds the library through its rpath, the second one
	// does not: the second one needs the cache in the image.
	paths := map[string]searchPaths{
		"/out/app":    {rpath: []string{"/opt/lib"}},
		"/out/worker": {},
	}

	var sb strings.Builder
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	warnNeedsCache(libs, paths)
	_ = w.Close()
	os.Stderr = old
	if _, err := io.Copy(&sb, r); err != nil {
		t.Fatal(err)
	}

	out := sb.String()
	if !strings.Contains(out, "/opt/lib/libfoo.so") {
		t.Errorf("warning = %q, want it to name the library", out)
	}
	if !strings.Contains(out, "/out/worker") {
		t.Errorf("warning = %q, want it to name the binary that needs the cache", out)
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
	abs, err := packageLibs(libs, nil, pkg)
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

	// a file from an earlier run stays: the tree is added to, so several
	// binaries can be packaged into it.
	stale := filepath.Join(pkg, "lib", "stale.so")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := packageLibs(libs, nil, pkg); err != nil {
		t.Fatalf("packageLibs() error = %v", err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("%s did not survive the second run", stale)
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
	if _, err := packageLibs(libs, nil, foreign); err == nil {
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
	if _, err := packageLibs(libs, nil, empty); err != nil {
		t.Errorf("packageLibs() refused an empty folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(empty, "lib/x86_64-linux-gnu/libc.so.6")); err != nil {
		t.Errorf("the library was not copied into the empty folder: %v", err)
	}
}

func TestPackageLibsPutsTheBinaryInTheTree(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "app")
	if err := os.WriteFile(bin, []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(dir, "rootfs")
	if _, err := packageLibs(nil, []string{bin}, pkg); err != nil {
		t.Fatalf("packageLibs() error = %v", err)
	}
	// the binary keeps the path it has in the builder
	abs, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(pkg, abs)
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
}

func TestPackageLibsAddsToTheTree(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "app")
	if err := os.WriteFile(first, []byte("one"), 0o755); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "worker")
	if err := os.WriteFile(second, []byte("two"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(dir, "rootfs")

	// two runs, two binaries, one tree
	if _, err := packageLibs(nil, []string{first}, pkg); err != nil {
		t.Fatalf("packageLibs() error = %v", err)
	}
	if _, err := packageLibs(nil, []string{second}, pkg); err != nil {
		t.Fatalf("packageLibs() error = %v", err)
	}
	for _, want := range []struct{ path, content string }{{first, "one"}, {second, "two"}} {
		abs, err := filepath.Abs(want.path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(pkg, abs))
		if err != nil {
			t.Fatalf("reading %s: %v", want.path, err)
		}
		if string(got) != want.content {
			t.Errorf("%s = %q, want %q", want.path, got, want.content)
		}
	}
}

func TestPackageLibsMakesNoFolderForAStaticBinary(t *testing.T) {
	// A static binary has no libraries, so there is nothing to copy: no
	// folder, and no COPY statement either.
	dir := t.TempDir()
	pkg := filepath.Join(dir, "rootfs")
	abs, err := packageLibs(nil, nil, pkg)
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
	if err := writePackage(&sb, nil, nil, pkg, "builder"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sb.String(), "COPY") {
		t.Errorf("output %q copies something, want nothing", sb.String())
	}
}

func TestPackageLibsRemovesLeftoverTempFiles(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "app")
	if err := os.WriteFile(bin, []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(dir, "rootfs")
	if err := os.MkdirAll(filepath.Join(pkg, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	// a run that was killed while copying leaves a temporary file behind
	leftover := filepath.Join(pkg, "lib", tempPrefix+"123456")
	if err := os.WriteFile(leftover, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := packageLibs(nil, []string{bin}, pkg); err != nil {
		t.Fatalf("packageLibs() error = %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("%s survived the run, want it removed", leftover)
	}
}

func TestPackageLibsRefusesAForeignMarkerFile(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "app")
	if err := os.WriteFile(bin, []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(dir, "rootfs")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file of the user with the name of the marker must not be
	// overwritten, not even when the folder next to it is empty.
	marker := pkg + markerName
	if err := os.WriteFile(marker, []byte("my own file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := packageLibs(nil, []string{bin}, pkg); err == nil {
		t.Error("packageLibs() wrote next to a file that is not ours")
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "my own file\n" {
		t.Errorf("the marker file was changed: %q", got)
	}
}

func TestPackageLibsRemovesLeftoverTempFilesInEveryFolder(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "app")
	if err := os.WriteFile(bin, []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(dir, "rootfs")
	// the first run makes the folder ours
	if _, err := packageLibs(nil, []string{bin}, pkg); err != nil {
		t.Fatalf("packageLibs() error = %v", err)
	}
	// a normal file in bin/, which the walk meets before lib/
	if err := os.MkdirAll(filepath.Join(pkg, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "bin", "app"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// a run that was killed while copying left a temporary file behind
	if err := os.MkdirAll(filepath.Join(pkg, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(pkg, "lib", tempPrefix+"123456")
	if err := os.WriteFile(leftover, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := packageLibs(nil, []string{bin}, pkg); err != nil {
		t.Fatalf("packageLibs() error = %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("%s survived the run, want it removed", leftover)
	}
}

func TestPackageLibsWritesNoSymlink(t *testing.T) {
	// kaniko does not copy symlinks, so the tree must not hold one: it is
	// copied with a single statement.
	dir := t.TempDir()
	target := filepath.Join(dir, "libc.so.6.1")
	if err := os.WriteFile(target, []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "libc.so.6")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(dir, "rootfs")
	libs := []lib{{kind: kindLib, soname: "libc.so.6", dest: "/lib/libc.so.6", src: link}}
	if _, err := packageLibs(libs, nil, pkg); err != nil {
		t.Fatalf("packageLibs() error = %v", err)
	}
	err := filepath.WalkDir(pkg, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			t.Errorf("%s is a symlink, want a regular file", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
	if err := writePackage(&sb, libs, nil, "/out/rootfs", "builder"); err != nil {
		t.Fatal(err)
	}
	want := "COPY --from=builder /out/rootfs/ /\n"
	if !strings.Contains(sb.String(), want) {
		t.Errorf("output %q does not contain %q", sb.String(), want)
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
