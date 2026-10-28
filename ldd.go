package main

import (
	"fmt"
	"regexp"
	"strings"
)

// dep is one entry of a dependency report.
type dep struct {
	soname string // empty for the ELF interpreter
	path   string // where the loader finds it
}

var (
	reVirtual = regexp.MustCompile(`^\s*(linux-vdso\.so\.1|linux-gate\.so\.1)\s+\(0x[0-9a-fA-F]+\)\s*$`)
	// The path may hold a space, the loader prints it as it is.
	reNamed = regexp.MustCompile(`^\s*(\S+)\s+=>\s+(/.+?)\s*(?:\(0x[0-9a-fA-F]+\))?\s*$`)
	rePath  = regexp.MustCompile(`^\s*(/.+?)\s+\(0x[0-9a-fA-F]+\)\s*$`)
	// The loader says which library it could not load, either as
	// "=> not found" or as an error.
	reMissing  = regexp.MustCompile(`^\s*(\S+)\s+=>\s+not found\s*$`)
	reGlibcErr = regexp.MustCompile(`(?i)error while loading shared libraries: ([^:]+):`)
	reMuslErr  = regexp.MustCompile(`(?i)error loading shared library ([^:]+):`)
)

// parseLdd understands the dependency list the glibc and the musl loader
// print.
func parseLdd(out string) ([]dep, error) {
	var deps []dep
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case reVirtual.MatchString(line):
			// the vDSO lives in the kernel, there is nothing to copy
		case reNamed.MatchString(line):
			m := reNamed.FindStringSubmatch(line)
			deps = append(deps, dep{soname: m[1], path: m[2]})
		case rePath.MatchString(line):
			deps = append(deps, dep{path: rePath.FindStringSubmatch(line)[1]})
		case strings.Contains(line, "=>"):
			// a dependency line we do not understand: fail loudly rather
			// than silently dropping a library from the image.
			return nil, fmt.Errorf("unrecognised ldd line %q", strings.TrimSpace(line))
		}
	}
	if len(deps) == 0 {
		return nil, fmt.Errorf("no shared libraries found")
	}
	return deps, nil
}

// missingLibrary finds the library the loader could not load, from what it
// printed on stdout ("=> not found") or on stderr ("cannot open shared
// object file"). It returns an empty string when it said nothing.
func missingLibrary(stdout, stderr string) string {
	for _, line := range strings.Split(stdout+"\n"+stderr, "\n") {
		for _, re := range []*regexp.Regexp{reMissing, reGlibcErr, reMuslErr} {
			if m := re.FindStringSubmatch(line); m != nil {
				return m[1]
			}
		}
	}
	return ""
}
