# minimalizer

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

From a release archive (linux, on amd64, arm64 or armv7 — every archive ships
a `checksums.txt`):

```bash
tar xzf minimalizer_<version>_<os>_<arch>.tar.gz
sudo install -m 0755 minimalizer /usr/local/bin/minimalizer
```

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
RUN go build -o /out/app ./cmd/app && minimalizer --package /out/rootfs /out/app

FROM scratch
COPY --from=builder /out/app /app
COPY --from=builder /out/rootfs/lib/ /lib/
COPY --from=builder /out/rootfs/lib64/ /lib64/
```

Once the image is pushed, the same works from a registry:

```dockerfile
COPY --from=ghcr.io/inful/minimalizer:latest /minimalizer /usr/local/bin/minimalizer
```

The binary is statically linked, so it runs in any stage, whatever libc that
stage has. The stage it runs *in* still needs the ELF interpreter of the
analysed binary, because that is where the binaries and their libraries live.

## Usage

```
minimalizer [flags] <binary> [binary...]

  --stage string    name of the source stage used in COPY --from (default "builder")
  --ld-cache        also copy /etc/ld.so.cache
  --json            print JSON instead of Dockerfile statements
  --package dir     copy the needed files into dir, laid out as the image root,
                    and print the statements that move them there
```

Several binaries can be given at once; the libraries are merged and
deduplicated. A statically linked binary produces no statements.

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
have to be in the final image, and prints the statements that move them there:

```
$ minimalizer --package /out/rootfs /out/app
# (ELF interpreter) /lib64/ld-linux-x86-64.so.2 <- /usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2
# libc.so.6 /lib/x86_64-linux-gnu/libc.so.6 <- /usr/lib/x86_64-linux-gnu/libc.so.6
# libpcre2-8.so.0 /lib/x86_64-linux-gnu/libpcre2-8.so.0 <- /usr/lib/x86_64-linux-gnu/libpcre2-8.so.0.11.2
COPY --from=builder /out/rootfs/lib/ /lib/
COPY --from=builder /out/rootfs/lib64/ /lib64/
```

```
/out/rootfs/lib64/ld-linux-x86-64.so.2
/out/rootfs/lib/x86_64-linux-gnu/libc.so.6
/out/rootfs/lib/x86_64-linux-gnu/libpcre2-8.so.0
```

Everything in that tree is a regular file (permissions are kept, so the
interpreter stays executable) and there is not a single symlink in it, which
is what kaniko needs. `COPY` of a directory copies its *contents*, so the
trailing slash on the source puts the files at the root of the image. There is
one statement per top folder, so nothing but the libraries ends up in the
image.

The folder is emptied on every run, so files from an earlier run never end up
in the image. To be sure it never touches something you care about, the tool
only replaces a folder that it made itself - it leaves a `.minimalizer` file
inside it - or a folder that is empty. Any other folder is refused:

```
minimalizer: refusing to write into /usr: it is not empty and minimalizer did not make it
```

The `.minimalizer` file is empty and never reaches the image.

That makes the whole thing run inside `docker build`:

```dockerfile
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY . .
COPY minimalizer /usr/local/bin/minimalizer
RUN go build -o /out/app ./cmd/app && minimalizer --package /out/rootfs /out/app

FROM scratch
COPY --from=builder /out/app /app
COPY --from=builder /out/rootfs/lib/ /lib/
COPY --from=builder /out/rootfs/lib64/ /lib64/
```

The trailing `RUN` prints the statements to the build log; copy them into the
final stage, or generate the whole final stage in one go:

```dockerfile
RUN { echo "FROM scratch"; echo "COPY --from=builder /out/app /app"; \
      minimalizer --package /out/rootfs /out/app | grep '^COPY'; } > /out/Dockerfile
```

The binary itself is not part of the output: where it ends up in the final
image is up to you.

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
4. Everything is deduplicated by destination path.

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
  when it sees such a library and the binary has no search path of its own
  that covers it.
- The analysed binary's dynamic loader is executed, as with `ldd`. Only run
  minimalizer on binaries you trust.
- The tool does not copy the binary itself, `/etc/resolv.conf`, CA
  certificates or anything else a program may need at runtime.
