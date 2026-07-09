# Build the crawler. `go mod tidy` resolves dependencies at build time
# (go.sum is generated in-image), so no local Go toolchain is required.
FROM golang:1.25-alpine AS build
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/crawler .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 crawler
USER crawler
COPY --from=build /out/crawler /usr/local/bin/crawler
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/crawler"]
