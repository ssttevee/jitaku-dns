FROM golang AS builder

WORKDIR /src

COPY main.go go.mod go.sum ./
COPY internal/ ./internal/

RUN go generate ./... && CGO_ENABLED=0 go build -ldflags "-s -extldflags '-static'" -trimpath -tags 'builtinassets osusergo netgo static_build' -o /main .

FROM gcr.io/distroless/static

COPY --from=builder /main /

LABEL org.opencontainers.image.source=https://github.com/ssttevee/jitaku-dns
LABEL org.opencontainers.image.description="JitakuDNS"
LABEL org.opencontainers.image.licenses=MIT

ENTRYPOINT ["/main"]
