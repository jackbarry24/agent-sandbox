FROM golang:1.23-alpine AS build

WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/streamer ./sidecar/cmd/streamer

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/streamer /usr/local/bin/streamer
ENTRYPOINT ["/usr/local/bin/streamer"]
