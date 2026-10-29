# minimalizer

[![CI](https://github.com/inful/minimalizer/actions/workflows/ci.yml/badge.svg)](https://github.com/inful/minimalizer/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/inful/minimalizer.svg)](https://github.com/inful/minimalizer/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/inful/minimalizer)](https://goreportcard.com/report/github.com/inful/minimalizer)

Lists the shared libraries a binary depends on and prints `COPY --from`
statements that bring them into a `FROM scratch` image.

The statements are safe for **kaniko**, which does not copy symlinks or
hardlinks: every library is resolved to the real file behind its symlinks and
copied *to the name the dynamic loader looks for*, so the resulting image
contains no symlinks at all.

```
$ minimalizer /out/app
# (ELF interpreter) /lib64/ld-linux-x86-64.so.2 <- /usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2
COPY --from=builder /usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2
# libc.so.6 /lib/x86_64-linux-gnu/libc.so.6 <- /usr/lib/x86_64-linux-gnu/libc.so.6
COPY --from=builder /usr/lib/x86_64-linux-gnu/libc.so.6 /lib/x86_64-linux-gnu/libc.so.6
# libpcre2-8.so.0 /lib/x86_64-linux-gnu/libpcre2-8.so.0 <- /usr/lib/x86_64-linux-gnu/libpcre2-8.so.0.11.2
COPY --from=builder /usr/lib/x86_64-linux-gnu/libpcre2-8.so.0.11.2 /lib/x86_64-linux-gnu/libpcre2-8.so.0
```

The comment above each statement shows what the binary uses: the soname, the
path the loader resolved it to, and the real file that gets copied.

## Install

From a release archive (linux, on amd64, arm64 or armv7 - every release has a
`checksums.txt` next to the archives):

```bash
tar xzf minimalizer_<version>_<os>_<arch>.tar.gz
sudo install -m 0755 minimalizer /usr/local/bin/minimalizer
```

The architecture is in the file name: `amd64`, `arm64` or `armv7`.

Or, from source:

```bash
go install github.com/inful/minimalizer@latest
# or
git clone https://github.com/inful/minimalizer && cd minimalizer && go build -o minimalizer .
```

The tool must run **in the stage that holds the binaries** (it reads that
stage's filesystem). It needs the ELF interpreter of the analysed binary,
which every stage that can *run* that binary has - the `ldd` command itself is
not needed, the interpreter is asked directly (see
[How it works](#how-it-works)). It only runs on Linux.

### Working on the tool

The checks that run before every commit are the ones the CI runs:
`golangci-lint` and `go test -race`. They are set up with
[lefthook](https://lefthook.dev):

```bash
brew install lefthook        # or the package of your system
git clone https://github.com/inful/minimalizer && cd minimalizer
lefthook install             # installs the git hook
```

### As an image

The repository ships a `Dockerfile` that builds the tool into an image of its
own - a static binary in a `scratch` image, about 3 MB:

```
docker build -t minimalizer-tool .
```

A build stage can then copy the binary out of that image instead of building
it again:

```dockerfile
FROM minimalizer-tool AS tool

FROM golang:1.26-bookworm AS builder
COPY --from=tool /minimalizer /usr/local/bin/minimalizer
RUN go build -o /app ./cmd/app && minimalizer --package /app

FROM scratch
COPY --from=builder /rootfs/ /
ENTRYPOINT ["/app"]
```

Once the image is pushed, the same works from a registry:

```dockerfile
COPY --from=ghcr.io/inful/minimalizer:latest /minimalizer /usr/local/bin/minimalizer
```

Every release publishes that image, for amd64, arm64 and armv7 in one
multi-arch tag, so the same line works on every machine. The image carries the
tag of the version, the tag of the series (`v0.1`) and `latest`. The last two
move when a release is published, so no tag points at a version that has no
release page yet.

The binary is statically linked, so it runs in any stage, whatever libc that
stage has. The stage it runs *in* still needs the ELF interpreter of the
analysed binary, because that is where the binaries and their libraries live.

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

Several binaries can be given at once; the libraries are merged and
deduplicated. A statically linked binary produces no statements - unless
`--package` is used, which copies the binary itself into the tree.

An argument with a `*`, `?` or `[` in it is a pattern, and every binary it
matches is analysed. A file that is not a binary and a folder are skipped, so
a pattern can point at the folder a distributor ships its binaries in:

```
$ minimalizer --package /app '/opt/vendor/bin/*'
# (ELF interpreter) /lib64/ld-linux-x86-64.so.2 <- /usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2
# libc.so.6 /lib/x86_64-linux-gnu/libc.so.6 <- /usr/lib/x86_64-linux-gnu/libc.so.6
# binary /opt/vendor/bin/app
# binary /opt/vendor/bin/worker
COPY --from=builder /rootfs/ /
```

A pattern that matches nothing is an error, so a typo cannot leave the image
without the binaries.

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

## Using it inside the build

`--package` writes the needed files into a directory, laid out exactly as they
have to be in the final image, and prints the statement that moves them there.
The flag takes no value: the binaries are the arguments, and the tree goes to
`/rootfs` unless you write `--package=DIR`.
Everything keeps the path it has in the builder - the libraries because that
is where the loader looks for them, the binary for the same reason: two
binaries with the same name in different folders do not overwrite each
other. The path of the binary in the image is therefore the path where it was
built.

```
$ minimalizer --package /app
# (ELF interpreter) /lib64/ld-linux-x86-64.so.2 <- /usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2
# libc.so.6 /lib/x86_64-linux-gnu/libc.so.6 <- /usr/lib/x86_64-linux-gnu/libc.so.6
# libpcre2-8.so.0 /lib/x86_64-linux-gnu/libpcre2-8.so.0 <- /usr/lib/x86_64-linux-gnu/libpcre2-8.so.0.11.2
# binary /app
COPY --from=builder /rootfs/ /
```

```
/rootfs/app
/rootfs/lib64/ld-linux-x86-64.so.2
/rootfs/lib/x86_64-linux-gnu/libc.so.6
/rootfs/lib/x86_64-linux-gnu/libpcre2-8.so.0
```

Everything in that tree is a regular file (permissions are kept, so the
interpreter stays executable) and there is not a single symlink in it, which
is what kaniko needs. `COPY` of a folder copies its *contents*, so the files
of the tree land at the root of the image.

One statement moves the whole tree, so the same Dockerfile builds every
platform: the top folders of the tree differ per platform (`lib64` on amd64,
`lib` on arm64), and a statement per folder would break on the other one.

The tree is added to, not replaced, so the tool can be run more than once to
package several binaries into the same folder:

```
RUN minimalizer --package /out/app
RUN minimalizer --package /out/worker
```

The tree goes to `/rootfs`, which needs write access to `/`. In a build
stage that runs as a user other than root, name another folder with
`--package=DIR`; the folder then shows up in the statement the tool prints.

To be sure it never touches something you care about, the tool only writes
into a folder that it made itself - it leaves a `<folder>.minimalizer` file
*next* to it - or a folder that is empty. Any other folder is refused:

```
minimalizer: refusing to write into /usr: it is not empty and minimalizer did not make it
```

The `<folder>.minimalizer` file lives next to the folder, so it never reaches
the image, and only a file with exactly the content the tool writes gives it
permission to replace the folder. A file of your own with that name is never
overwritten: the tool refuses the run instead.

That makes the whole thing run inside `docker build`:

```dockerfile
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY . .
COPY minimalizer /usr/local/bin/minimalizer
RUN go build -o /app ./cmd/app && minimalizer --package /app

FROM scratch
COPY --from=builder /rootfs/ /
ENTRYPOINT ["/app"]
```

The trailing `RUN` prints the statements to the build log; copy them into the
final stage, or generate the whole final stage in one go:

```dockerfile
RUN { echo "FROM scratch"; \
      minimalizer --package /app | grep '^COPY'; } > /out/Dockerfile
```

Without `--package` the binary is not part of the output: where it ends up in
the final image is up to you.

### Without --package

The default output is one `COPY --from` per library, which is more explicit
and lets you pick the stage name:

```dockerfile
FROM scratch
COPY --from=builder /out/app /app
COPY --from=builder /usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2
COPY --from=builder /usr/lib/x86_64-linux-gnu/libc.so.6 /lib/x86_64-linux-gnu/libc.so.6
```

## How it works

1. The ELF interpreter of the binary is found (its `.interp` section, read with
   `debug/elf` from the standard library) and run with `--list`, which is what
   `ldd(1)` does under the hood. The loader reports the shared objects the
   binary needs, transitively. No `ldd` command has to exist in the image.
   A binary that asks for no library at all is static, and the loader is not
   started for it.
2. Every reported path is resolved with `filepath.EvalSymlinks`, so the
   source of the `COPY` is always a regular file.
3. The destination stays the path the loader resolved the library to, so the
   loader finds it under the name it expects - with the real file's contents,
   which is what kaniko needs.
4. Everything is deduplicated by destination path: a library that several
   binaries need is resolved and copied once.

The ELF interpreter (`/lib64/ld-linux-x86-64.so.2` and friends) is included,
because the kernel loads it by absolute path.

## Caveats

- The tool only runs on Linux. It asks the ELF interpreter of the binary, and
  that only exists there.
- Only the libraries that are loaded when the program starts are reported.
  A library that the program opens later, while it runs, is not in the list:
  Python C extensions, Go plugins and anything else that uses `dlopen` have to
  be copied by hand.
- A library the loader cannot resolve is an error, not a warning: the image
  would fail at runtime. Install the library in the builder stage first.
- The `LD_*` variables of the build stage are ignored while the loader runs,
  so the answer does not depend on the environment of the build stage.
- Without `/etc/ld.so.cache` the loader falls back to its built-in search
  path. Libraries outside `/lib`, `/usr/lib`, `/lib64`, `/usr/lib64` and
  `/usr/local/lib` (for example in `/opt`) are only found again if the cache
  is copied too, so use `--ld-cache` in that case. The tool prints a warning
  for every library that the loader can only find through its cache. A search
  path of the binary covers the libraries it asks for itself; only `DT_RPATH`
  is passed on to the libraries of a library, a `DT_RUNPATH` is not - so a
  library of a library can need the cache even though the binary has a
  runpath that covers its folder.
- A path that cannot be written in a Dockerfile line - one with a space, a
  quote, a newline or a `[` in it - is refused in the modes that print a
  `COPY` line per library, because Docker would read the line differently.
  The `--package` mode copies the whole tree with a single statement, so a
  path with a space is fine there.
- The stage name has to be a name Docker accepts for a stage (letters,
  digits, `_`, `.` and `-`). A name with a colon would be read by Docker as
  the name of an image, so it is refused.
- A pattern is expanded by the tool. The shell expands it before the tool
  sees it when it is not quoted, so quote a pattern that the tool should
  expand. The glob of the standard library matches one level of folders:
  `**` is refused, because it would match fewer binaries than it looks.
- The analysed binary's dynamic loader is executed, as with `ldd`. Only run
  minimalizer on binaries you trust.
- `--package` copies the binaries into the tree at the path they have in the
  builder; without it the tool does not copy the binary itself. Neither mode
  copies `/etc/resolv.conf`, CA certificates or anything else a program may
  need at runtime.
