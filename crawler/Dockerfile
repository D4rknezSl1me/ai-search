# Build the crawler skeleton. Stdlib only → no module downloads needed.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/crawler .

FROM alpine:3.20
RUN adduser -D -u 10001 crawler
USER crawler
COPY --from=build /out/crawler /usr/local/bin/crawler
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/crawler"]
