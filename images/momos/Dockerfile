# momos: the service (plan.md §3). Pure-Go SQLite (modernc) means no cgo — a
# static binary on a minimal base.
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/momos ./cmd/momos

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/momos /app/momos
EXPOSE 8080
ENTRYPOINT ["/app/momos"]
