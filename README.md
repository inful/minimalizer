# minimalizer

[![CI](https://github.com/inful/minimalizer/actions/workflows/ci.yml/badge.svg)](https://github.com/inful/minimalizer/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/inful/minimalizer.svg)](https://github.com/inful/minimalizer/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/inful/minimalizer)](https://goreportcard.com/report/github.com/inful/minimalizer)

Lists the shared libraries a binary depends on and prints the `COPY --from`
statements that bring them into a `FROM scratch` image.

Every library is resolved to the real file behind its symlinks and copied to
the path the dynamic loader looks for, so the image holds regular files where
the loader wants them. Copying that path yourself does not work: it runs
through symlinks, and the builder either copies the link instead of the file,
or does not find it at all.

## The use case

You build an app in a builder stage and want it in a scratch image, together
with the system tools it needs - `bash`, `ssh`, `curl`. A scratch image has
nothing in it, so the libraries of those tools have to be copied in as well.
`minimalizer` works out which ones and prints the statements that copy them.

The tool comes out of its own image, so the build needs nothing else:

```dockerfile
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY . .
RUN go build -o /app ./cmd/app

FROM debian:bookworm-slim AS tools
COPY --from=ghcr.io/inful/minimalizer:latest /minimalizer /usr/local/bin/minimalizer
RUN minimalizer --package=/tools /bin/bash

FROM scratch
COPY --from=builder /app /app
COPY --from=tools /tools/ /
ENTRYPOINT ["/app"]
```

`minimalizer` runs in the stage that holds the binaries - it reads that
stage's filesystem - so the tools stage is the one that needs it. The tools
have to be in that stage too: install them there.

The image ends up with the tools and the libraries they needed at that
moment, and nothing else. Nothing in a scratch image is upgraded underneath
you: when the distribution ships a new bash or a new libc, it cannot change
an image you already built, and the statements name every file that is in it.
That is what keeps the dependencies of a new upstream release from reaching
you - a runtime image like `FROM debian` brings its own libraries, and a new
release of it can break an app that worked yesterday.

## Where this fits

[distroless](https://github.com/GoogleContainerTools/distroless) gives you a
base image: a fixed set of libraries and no shell. That is the right base for
an image that only runs your app.

`minimalizer` covers what a base image cannot: the exact libraries your own
binaries need, and the system tools you want next to them. The two go
together - a distroless base for the app, minimalizer for what you add to it.
(The libraries it resolves come from the stage it runs in, so a tool you copy
has to come from a distribution as new as the base image you build on.)

| You want | Use |
|---|---|
| just your app | `FROM distroless/static`, or `FROM scratch` |
| your app and `bash`, `ssh`, `curl` | `minimalizer` |
| the exact libraries of your own build | `minimalizer` |

## Install

The image is the usual way to get the tool:

```dockerfile
COPY --from=ghcr.io/inful/minimalizer:latest /minimalizer /usr/local/bin/minimalizer
```

It holds a static binary, so it runs in any stage, whatever libc that stage
has. Every release publishes it for amd64, arm64 and armv7 in one multi-arch
tag, so the same line works on every machine. The image carries the tag of
the version, the tag of the series it belongs to, and `latest`. The last two
move when a release is published, so no tag points at a version that has no
release page yet.

To build the image yourself instead of pulling it: `docker build -t
minimalizer-tool .` — the repository ships the Dockerfile for it.

Or from a release archive (linux, on amd64, arm64 or armv7 - every release has
a `checksums.txt` next to the archives):

```bash
tar xzf minimalizer_<version>_<os>_<arch>.tar.gz
sudo install -m 0755 minimalizer /usr/local/bin/minimalizer
```

Or from source:

```bash
go install github.com/inful/minimalizer@latest
```

The tool only runs on Linux: it asks the ELF interpreter of the binary for its
libraries.

## Usage

```
minimalizer [flags] <binary|pattern> [binary|pattern...]

  -json
    	print JSON instead of Dockerfile statements
  -ld-cache
    	also copy /etc/ld.so.cache (needed when libraries live outside the loader's default search path)
  -package
    	copy the binaries and their libraries into /rootfs, laid out as the image root (write --package=DIR for another folder)
  -stage string
    	name of the source stage used in COPY --from (default "builder")
  -version
    	print the version and exit

An argument with a * in it is a pattern: every binary it matches is analysed.
```

It exits 2 when the command is wrong, and 1 when the run failed — a library
the loader cannot resolve, for example.

`--package` writes the files into a tree, laid out as they have to be in the
final image, and prints the statement that moves the tree there. Everything
keeps the path it has in the builder, so two binaries with the same name in
different folders do not overwrite each other. The tree holds no symlinks, so
no builder has to follow one.

The flag takes no value: the arguments are the binaries, and the tree goes to
`/rootfs`, which needs write access to `/`. Write `--package=DIR` to name
another folder - `--package DIR` would read `DIR` as a binary, and the tool
says so.

To be sure it never touches something you care about, the tool only writes
into a folder that it made itself - it leaves a `<folder>.minimalizer` file
*next* to it - or a folder that is empty. Any other folder is refused. The
marker file lives next to the folder, so it never reaches the image, and only
a file with exactly the content the tool writes gives it permission to replace
the folder. A file of your own with that name is never overwritten: the tool
refuses the run instead.

The tree is added to, not replaced, so the tool can be run more than once to
package several binaries into the same folder.

An argument with a `*`, `?` or `[` in it is a pattern, and every binary it
matches is analysed - handy for the folder of binaries a distributor ships.
The binaries a pattern matched are named on stderr, so a pattern that matches
less than you think is visible. A pattern that matches nothing, or nothing
but files that are not binaries, is an error, so a typo cannot leave the image
without the binaries.

Without `--package` the tool prints one `COPY --from` per library instead,
which is more explicit and lets you pick the stage name:

```
# (ELF interpreter) /lib64/ld-linux-x86-64.so.2 <- /usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2
COPY --from=builder /usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2
# libc.so.6 /lib/x86_64-linux-gnu/libc.so.6 <- /usr/lib/x86_64-linux-gnu/libc.so.6
COPY --from=builder /usr/lib/x86_64-linux-gnu/libc.so.6 /lib/x86_64-linux-gnu/libc.so.6
```

The comment above each statement shows what the binary uses: the soname, the
path the loader resolved it to, and the real file that gets copied.

`--json` returns the same information in machine readable form:

```json
{
  "binaries": ["/out/app"],
  "libraries": [
    {
      "kind": "library",
      "soname": "libc.so.6",
      "path": "/lib/x86_64-linux-gnu/libc.so.6",
      "realPath": "/usr/lib/x86_64-linux-gnu/libc.so.6",
      "size": 1651408,
      "usedBy": ["/out/app"],
      "copy": "COPY --from=builder /usr/lib/x86_64-linux-gnu/libc.so.6 /lib/x86_64-linux-gnu/libc.so.6"
    }
  ]
}
```

## How it works

The ELF interpreter of the binary is found (its `.interp` section, read with
`debug/elf` from the standard library) and run with `--list`, which is what
`ldd(1)` does under the hood - no `ldd` command has to exist in the image.
Every reported path is resolved with `filepath.EvalSymlinks`, so the source of
the `COPY` is always a regular file, and the destination stays the path the
loader resolved the library to, so the loader finds it under the name it
expects. Everything is deduplicated by destination path.

## Caveats

- Only the libraries that are loaded when the program starts are reported. A
  library that the program opens later, while it runs, is not in the list:
  Python C extensions, Go plugins and anything else that uses `dlopen` have to
  be copied by hand.
- A library the loader cannot resolve is an error, not a warning: the image
  would fail at runtime. Install the library in the builder stage first.
- The `LD_*` variables of the build stage are ignored while the loader runs,
  so the answer does not depend on the environment of the build stage.
- Without `/etc/ld.so.cache` the loader falls back to its built-in search
  path. Libraries outside `/lib`, `/usr/lib`, `/lib64`, `/usr/lib64`,
  `/usr/local/lib` and `/usr/local/lib64` (for example in `/opt`) are only
  found again if the cache is copied too, so use `--ld-cache` in that case.
  The tool prints a warning
  for every library that the loader can only find through its cache. A search
  path of the binary covers the libraries it asks for itself; only `DT_RPATH`
  is passed on to the libraries of a library, a `DT_RUNPATH` is not.
- A path that cannot be written in a Dockerfile line - one with a space, a
  quote, a newline or a `[` in it - is refused where it goes into a `COPY`
  line, because Docker would read the line differently.
- The stage name has to be a name Docker accepts for a stage: it starts with
  a letter or a digit, and holds letters, digits, `_`, `.` and `-`.
- The analysed binary's dynamic loader is executed, as with `ldd`. Only run
  minimalizer on binaries you trust.
- A pattern is expanded by the tool. The shell expands it before the tool
  sees it when it is not quoted, so quote a pattern that the tool should
  expand. The glob of the standard library matches one level of folders:
  `**` is refused, because it would match fewer binaries than it looks.
- The statements work with every builder - BuildKit, podman, buildah and
  kaniko. kaniko is archived and no longer developed, but it is still in use,
  and it is where this problem came from.

## Working on the tool

Before every commit, the shared lefthook configuration runs `gofmt`, `go vet`,
`golangci-lint`, `go test -race` and `govulncheck`, and for the other files
`markdownlint`, `typos`, `gitleaks` and `shellcheck`. The CI runs vet, lint
and the tests on every push. The hooks are set up with
[lefthook](https://lefthook.dev):

```bash
brew install lefthook        # or the package of your system
git clone https://github.com/inful/minimalizer && cd minimalizer
lefthook install             # installs the git hook
```
