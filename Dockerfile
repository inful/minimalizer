# minimalizer packaged as an image of its own, so a build stage can copy the
# binary out of it:
#
#   COPY --from=ghcr.io/inful/minimalizer:latest /minimalizer /usr/local/bin/minimalizer
#
# The binary is statically linked (CGO_ENABLED=0), so it runs in any stage,
# whatever its libc is.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /minimalizer .

FROM scratch
COPY --from=build /minimalizer /minimalizer
LABEL org.opencontainers.image.source="https://github.com/inful/minimalizer"
