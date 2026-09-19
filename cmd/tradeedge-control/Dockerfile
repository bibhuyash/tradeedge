FROM golang:1.23.4-bookworm@sha256:95db116434e3f21a2a15600ffc7169bf380c6bfd021b154d106fcb346721c277 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -o /out/tradeedge-control ./cmd/tradeedge-control && \
    CGO_ENABLED=0 go build -buildvcs=false -o /out/tradeedge-validation ./cmd/tradeedge-validation

FROM docker:27-cli
RUN apk add --no-cache git curl tzdata && \
    git config --system --add safe.directory /workspace && \
    git config --system core.autocrlf true
COPY --from=build /out/tradeedge-control /usr/local/bin/tradeedge-control
COPY --from=build /out/tradeedge-validation /usr/local/bin/tradeedge-validation
WORKDIR /workspace
ENTRYPOINT ["tradeedge-control"]
