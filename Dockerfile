FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cue-vision ./cmd/cue-vision

FROM alpine:3.22
RUN adduser -D -u 10001 app
USER app
COPY --from=build /out/cue-vision /usr/local/bin/cue-vision
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s \
    CMD wget -qO- http://127.0.0.1:8080/readyz >/dev/null || exit 1
ENTRYPOINT ["cue-vision"]
