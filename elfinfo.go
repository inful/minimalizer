package main

import (
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"strings"
)

// elfInfo is what the tool needs to know about the binary itself.
type elfInfo struct {
	interp  string      // the ELF interpreter, empty for a statically linked binary
	needed  []string    // the libraries the binary asks for
	rpath   []string    // DT_RPATH: the libraries of the binary inherit it
	runpath []string    // DT_RUNPATH: only the libraries the binary asks for
	machine elf.Machine // the architecture the binary is built for
}

// readELF reads the parts of the binary we care about. It refuses anything
// that is not an ELF file, so a script is never handed to the loader.
func readELF(path string) (elfInfo, error) {
	f, err := elf.Open(path)
	if err != nil {
		var formatErr *elf.FormatError
		if errors.As(err, &formatErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return elfInfo{}, fmt.Errorf("not an ELF binary")
		}
		return elfInfo{}, err
	}
	defer func() { _ = f.Close() }()

	info := elfInfo{machine: f.Machine}
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		data, err := io.ReadAll(p.Open())
		if err != nil {
			return elfInfo{}, fmt.Errorf("reading the ELF interpreter: %w", err)
		}
		info.interp = strings.TrimRight(string(data), "\x00")
	}
	for _, tag := range []elf.DynTag{elf.DT_NEEDED, elf.DT_RPATH, elf.DT_RUNPATH} {
		values, err := f.DynString(tag)
		if err != nil {
			continue
		}
		switch tag {
		case elf.DT_NEEDED:
			info.needed = values
		case elf.DT_RPATH:
			for _, v := range values {
				info.rpath = append(info.rpath, strings.Split(v, ":")...)
			}
		case elf.DT_RUNPATH:
			for _, v := range values {
				info.runpath = append(info.runpath, strings.Split(v, ":")...)
			}
		}
	}
	return info, nil
}
