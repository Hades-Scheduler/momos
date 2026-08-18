# momos-publisher: the universal reporter (plan.md §11.7). bash + ca-certs so it
# runs under both Hades executors and can reach the forge + callback over TLS.
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/publisher ./cmd/publisher

FROM alpine:3.20
RUN apk add --no-cache bash ca-certificates
COPY --from=build /out/publisher /app/publisher
ENTRYPOINT ["/app/publisher"]
