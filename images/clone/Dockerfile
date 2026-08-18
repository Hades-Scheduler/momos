# momos-clone: interim shell clone step (plan.md §12.2). git + bash so it runs
# under both Hades executors (Docker uses /bin/bash, plan.md §12.1.3).
FROM alpine:3.20
RUN apk add --no-cache git bash ca-certificates
COPY images/clone/clone.sh /app/clone.sh
RUN chmod +x /app/clone.sh
# The Hades step sets script="/app/clone.sh"; ENTRYPOINT is a convenience for
# running the image directly.
ENTRYPOINT ["/app/clone.sh"]
