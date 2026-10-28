package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

const (
	kindLib    = "library"
	kindInterp = "interpreter"
	kindCache  = "cache"
)

// badPathChars would break the Dockerfile lines we print.
const badPathChars = " \t\n#\"'$"

// badCommentChars would break out of a comment line we print.
const badCommentChars = "\r\n"

// tempPrefix is the name of the temporary file copyFile writes next to its
// destination. A run that is killed leaves it behind.
const tempPrefix = ".minimalizer-tmp-"

// markerName is the file next to the package folder that says the folder
// belongs to minimalizer, so it may be replaced on the next run.
//
// It lives NEXT TO the folder on purpose. Inside it, it would be copied into
// the image, and the statements would have to name every top folder of the
// tree — and those differ per platform (lib64 on amd64, lib on arm64), so
// one Dockerfile could not build both.
//
// The content is checked as well: a file that happens to have the same name
// does not give the tool permission to delete anything.
const markerName = ".minimalizer"

// markerContent marks a folder as minimalizer's own.
const markerContent = "minimalizer package folder\n"

// lib is one file that has to end up in the final image.
type lib struct {
	kind   string
	soname string
	dest   string // path the dynamic loader looks for
	src    string // real file in the builder stage
	size   int64
	usedBy []string
}

// defaultDirs are the directories the dynamic loader searches when there is
// neither a cache nor a search path in the binary to guide it.
var defaultDirs = []string{"/lib", "/usr/lib", "/lib64", "/usr/lib64", "/usr/local/lib", "/usr/local/lib64"}

// collect resolves the shared libraries of every binary into a deduplicated
// and deterministic list.
func collect(binaries []string, withCache bool) ([]lib, error) {
	byDest := map[string]lib{}
	// The search paths of each binary, so a library is judged against the
	// binary that needs it and not against the ones that happen to be
	// analysed in the same run.
	rpaths := map[string][]string{}
	for _, bin := range binaries {
		info, err := readELF(bin)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", bin, err)
		}
		rpaths[bin] = info.rpaths

		deps, err := lddDeps(bin, info)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", bin, err)
		}
		if err := merge(byDest, deps, bin); err != nil {
			return nil, err
		}
	}

	libs := make([]lib, 0, len(byDest))
	for _, l := range byDest {
		libs = append(libs, l)
	}
	if withCache {
		c, err := resolveCache()
		if err != nil {
			return nil, err
		}
		if c != nil {
			libs = append(libs, *c)
		}
	}
	sort.Slice(libs, func(i, j int) bool {
		if rank(libs[i]) != rank(libs[j]) {
			return rank(libs[i]) < rank(libs[j])
		}
		return libs[i].dest < libs[j].dest
	})

	warnNeedsCache(libs, rpaths)
	return libs, nil
}

// warnNeedsCache warns about every library that the loader can only find
// through its cache. The search paths of each binary are looked at on its
// own: a library that one binary finds through its rpath can still be a
// problem for another binary that has none.
func warnNeedsCache(libs []lib, rpaths map[string][]string) {
	for _, l := range libs {
		if l.kind != kindLib {
			continue
		}
		for _, bin := range l.usedBy {
			if needsCache(l.dest, rpaths[bin]) {
				fmt.Fprintf(os.Stderr, "minimalizer: warning: %s is outside the loader's default search path of %s, the image may need --ld-cache\n", l.dest, bin)
				break
			}
		}
	}
}

// merge adds the dependencies of one binary to the files that have to be
// copied. A file that is already known keeps its first source, and the list
// of binaries that need it grows.
func merge(byDest map[string]lib, deps []dep, bin string) error {
	for _, d := range deps {
		if prev, ok := byDest[d.path]; ok {
			// Already resolved for another binary: only the list of
			// users grows. Resolving it again would repeat the same
			// file system work for every binary.
			if !slices.Contains(prev.usedBy, bin) {
				prev.usedBy = append(prev.usedBy, bin)
			}
			byDest[d.path] = prev
			continue
		}
		l, err := resolve(d)
		if err != nil {
			return fmt.Errorf("%s: %w", bin, err)
		}
		l.usedBy = []string{bin}
		byDest[l.dest] = l
	}
	return nil
}

// resolve turns an ldd entry into a copy instruction: the destination is the
// path the dynamic loader looks for, the source is the real file behind any
// symlink, because kaniko does not copy symlinks.
func resolve(d dep) (lib, error) {
	if strings.ContainsAny(d.path, badPathChars) {
		return lib{}, fmt.Errorf("unsupported library path %q", d.path)
	}
	resolved, err := filepath.EvalSymlinks(d.path)
	if err != nil {
		return lib{}, fmt.Errorf("cannot resolve %s: %w", d.path, err)
	}
	// The real file is what the COPY statement names, so it has to be
	// printable in a Dockerfile line as well.
	if strings.ContainsAny(resolved, badPathChars) {
		return lib{}, fmt.Errorf("unsupported library path %q", resolved)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return lib{}, fmt.Errorf("cannot stat %s: %w", resolved, err)
	}
	if !fi.Mode().IsRegular() {
		return lib{}, fmt.Errorf("%s is not a regular file", resolved)
	}
	kind := kindLib
	if d.soname == "" {
		kind = kindInterp
	}
	return lib{kind: kind, soname: d.soname, dest: d.path, src: resolved, size: fi.Size()}, nil
}

// resolveCache resolves the loader cache, if the stage has one.
func resolveCache() (*lib, error) {
	const path = "/etc/ld.so.cache"
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot stat %s: %w", path, err)
	}
	l, err := resolve(dep{path: path})
	if err != nil {
		return nil, err
	}
	l.kind = kindCache
	return &l, nil
}

// rank orders the output: the interpreter first, then the loader cache, then
// the libraries.
func rank(l lib) int {
	switch l.kind {
	case kindInterp:
		return 0
	case kindCache:
		return 1
	default:
		return 2
	}
}

// needsCache reports whether the loader can only find a library through its
// cache: it is neither in a default folder nor in a search path of the binary.
func needsCache(dest string, searchPaths []string) bool {
	return !underAny(dest, defaultDirs) && !underAny(dest, searchPaths)
}

// underAny reports whether a path is inside one of the folders.
func underAny(path string, dirs []string) bool {
	for _, dir := range dirs {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}

// checkWord makes sure a word we write into a Dockerfile line cannot break
// that line, and that it is not another flag in disguise.
func checkWord(what, value string) error {
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("the %s %q starts with a dash, write it as --flag=value", what, value)
	}
	if strings.ContainsAny(value, badPathChars) {
		return fmt.Errorf("the %s %q cannot be used in a Dockerfile line", what, value)
	}
	return nil
}

// label describes a library in the generated comments.
func label(l lib) string {
	switch l.kind {
	case kindInterp:
		return "(ELF interpreter)"
	case kindCache:
		return "(loader cache)"
	default:
		return l.soname
	}
}

// writeComments prints one comment line per library.
func writeComments(w io.Writer, libs []lib) error {
	for _, l := range libs {
		if _, err := fmt.Fprintf(w, "# %s %s <- %s\n", label(l), l.dest, l.src); err != nil {
			return err
		}
	}
	return nil
}

// writeDockerfile prints comments and COPY statements that can be pasted into
// a Dockerfile.
func writeDockerfile(w io.Writer, libs []lib, stage string) error {
	if err := writeComments(w, libs); err != nil {
		return err
	}
	for _, l := range libs {
		if _, err := fmt.Fprintln(w, copyStatement(l, stage)); err != nil {
			return err
		}
	}
	return nil
}

// packageLibs copies the libraries and the binaries into dir, laid out as
// they have to be in the final image. The whole tree can then be moved into
// the scratch image with a single COPY --from, and it contains no symlinks.
//
// The tree is added to, not replaced, so several binaries can be packaged
// into the same folder by running the tool more than once. A folder that
// minimalizer did not make is never touched.
func packageLibs(libs []lib, binaries []string, dir string) (string, error) {
	treeAbs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %w", dir, err)
	}
	if len(libs) == 0 && len(binaries) == 0 {
		// Nothing to copy, so there is no folder to make.
		return treeAbs, nil
	}
	leftovers, err := prepareDir(treeAbs)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(treeAbs, 0o755); err != nil {
		if errors.Is(err, os.ErrPermission) && treeAbs == defaultRootfs {
			return "", fmt.Errorf("creating %s: %w (the default tree needs write access to /; use --package=DIR for another folder)", treeAbs, err)
		}
		return "", fmt.Errorf("creating %s: %w", treeAbs, err)
	}
	// A run that was killed while copying left temporary files behind.
	// They would end up in the image, so they go before anything is copied.
	for _, leftover := range leftovers {
		if err := os.Remove(leftover); err != nil {
			return "", fmt.Errorf("removing %s: %w", leftover, err)
		}
	}
	// Mark the folder before filling it, so a run that fails in the middle
	// can still be added to by the next one.
	if err := os.WriteFile(treeAbs+markerName, []byte(markerContent), 0o644); err != nil {
		return "", fmt.Errorf("writing the marker: %w", err)
	}
	for _, l := range libs {
		dst := filepath.Join(treeAbs, l.dest)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
		}
		if err := copyFile(l.src, dst); err != nil {
			return "", fmt.Errorf("copying %s: %w", l.src, err)
		}
	}
	for _, bin := range binaries {
		// The binary keeps the path it has in the builder, like the
		// libraries do. Two binaries with the same name therefore land in
		// different places, and the path in the image is the path where
		// the binary was built.
		binAbs, err := filepath.Abs(bin)
		if err != nil {
			return "", fmt.Errorf("cannot resolve %s: %w", bin, err)
		}
		dst := filepath.Join(treeAbs, binAbs)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
		}
		if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
			return "", fmt.Errorf("%s is a folder in %s, cannot put the binary there", dst, treeAbs)
		}
		if err := copyFile(binAbs, dst); err != nil {
			return "", fmt.Errorf("copying %s: %w", bin, err)
		}
	}
	return treeAbs, nil
}

// prepareDir makes sure dir is a folder that belongs to minimalizer. It
// returns the temporary files of a run that was killed, which the caller
// has to delete before it copies anything.
func prepareDir(dir string) ([]string, error) {
	marker := dir + markerName
	ours := false
	if data, err := os.ReadFile(marker); err == nil {
		if string(data) != markerContent {
			// The file next to the folder is not ours. It must not be
			// overwritten either.
			return nil, fmt.Errorf("refusing to write into %s: %s is not a minimalizer marker file", dir, marker)
		}
		// we made this folder before, so we may add to it
		ours = true
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading %s: %w", marker, err)
	} else {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("reading %s: %w", dir, err)
		}
		if len(entries) == 0 {
			return nil, nil
		}
	}
	// The folder is ours, or it holds nothing but the temporary files of a
	// run that was killed. Those are empty as far as the user is concerned.
	leftovers, onlyLeftovers, err := scanTree(dir)
	if err != nil {
		return nil, err
	}
	if !ours && !onlyLeftovers {
		return nil, fmt.Errorf("refusing to write into %s: it is not empty and minimalizer did not make it", dir)
	}
	return leftovers, nil
}

// scanTree walks dir once. It returns the temporary files of a run that was
// killed and whether the folder holds nothing else.
func scanTree(dir string) (leftovers []string, onlyLeftovers bool, err error) {
	onlyLeftovers = true
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), tempPrefix) {
			leftovers = append(leftovers, path)
			return nil
		}
		onlyLeftovers = false
		return fs.SkipAll
	})
	if err != nil {
		return nil, false, err
	}
	return leftovers, onlyLeftovers, nil
}

// writePackage prints the statement that moves the packaged files into the
// final image. One statement for the whole tree, so the same Dockerfile
// builds every platform: the top folders of the tree differ per platform.
func writePackage(w io.Writer, libs []lib, binaries []string, dir, stage string) error {
	if len(libs) == 0 && len(binaries) == 0 {
		// Nothing to copy: the binary is static, and there is no folder
		// to point at.
		return nil
	}
	if err := writeComments(w, libs); err != nil {
		return err
	}
	for _, bin := range binaries {
		abs, err := filepath.Abs(bin)
		if err != nil {
			return err
		}
		// The path is printed in a comment, so it must not be able to
		// break out of it.
		if strings.ContainsAny(abs, badCommentChars) {
			return fmt.Errorf("the binary path %q cannot be used in a Dockerfile comment", abs)
		}
		if _, err := fmt.Fprintf(w, "# binary %s\n", abs); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "COPY --from=%s %s/ /\n", stage, dir)
	return err
}

// copyFile copies a file, keeping its permissions. It writes to a temporary
// file and renames it over the destination, so copying a file onto itself
// cannot empty it.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), tempPrefix+"*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// jsonLib is one entry of the JSON report.
type jsonLib struct {
	Kind     string   `json:"kind"`
	Soname   string   `json:"soname,omitempty"`
	Path     string   `json:"path"`
	RealPath string   `json:"realPath"`
	Size     int64    `json:"size"`
	UsedBy   []string `json:"usedBy,omitempty"`
	Copy     string   `json:"copy"`
}

// jsonReport is the whole JSON report.
type jsonReport struct {
	Binaries  []string  `json:"binaries"`
	Libraries []jsonLib `json:"libraries"`
}

// writeJSON prints the report as JSON.
func writeJSON(w io.Writer, binaries []string, libs []lib, stage string) error {
	report := jsonReport{Binaries: binaries, Libraries: make([]jsonLib, 0, len(libs))}
	for _, l := range libs {
		report.Libraries = append(report.Libraries, jsonLib{
			Kind:     l.kind,
			Soname:   l.soname,
			Path:     l.dest,
			RealPath: l.src,
			Size:     l.size,
			UsedBy:   l.usedBy,
			Copy:     copyStatement(l, stage),
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// copyStatement is the Dockerfile statement that installs a library.
func copyStatement(l lib, stage string) string {
	return fmt.Sprintf("COPY --from=%s %s %s", stage, l.src, l.dest)
}
