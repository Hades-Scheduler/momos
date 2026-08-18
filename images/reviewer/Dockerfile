# momos-reviewer: one image, oneshot + agentic (plan.md §11.3). Carries git so
# it computes its own diff and (agentic) navigates the tree. bash so it runs
# under both Hades executors. No forge credentials at runtime (§11.4).
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/reviewer ./cmd/reviewer

FROM alpine:3.20
RUN apk add --no-cache git bash ca-certificates
COPY --from=build /out/reviewer /app/reviewer
# Hades sets script="/app/reviewer" (non-empty so continue_on_error is honored, §12.1).
ENTRYPOINT ["/app/reviewer"]
