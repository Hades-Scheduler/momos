# Momos build/test/image targets.
REGISTRY ?= localhost:5000
TAG      ?= latest
IMAGES   := clone reviewer publisher momos

.PHONY: all build test vet tidy lint images push run clean

all: build test

build: ## build all binaries into ./bin
	CGO_ENABLED=0 go build -o bin/momos     ./cmd/momos
	CGO_ENABLED=0 go build -o bin/reviewer  ./cmd/reviewer
	CGO_ENABLED=0 go build -o bin/publisher ./cmd/publisher

test: ## run the full test suite
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

images: ## build the four container images
	docker build -f images/clone/Dockerfile     -t $(REGISTRY)/momos-clone:$(TAG)     .
	docker build -f images/reviewer/Dockerfile  -t $(REGISTRY)/momos-reviewer:$(TAG)  .
	docker build -f images/publisher/Dockerfile -t $(REGISTRY)/momos-publisher:$(TAG) .
	docker build -f images/momos/Dockerfile     -t $(REGISTRY)/momos:$(TAG)           .

push: images ## build and push the images to $(REGISTRY)
	docker push $(REGISTRY)/momos-clone:$(TAG)
	docker push $(REGISTRY)/momos-reviewer:$(TAG)
	docker push $(REGISTRY)/momos-publisher:$(TAG)
	docker push $(REGISTRY)/momos:$(TAG)

run: ## run the service locally against deploy/config.example.yaml
	MOMOS_TOKEN_SECRET=$${MOMOS_TOKEN_SECRET:-dev-secret} \
		go run ./cmd/momos -config deploy/config.example.yaml -prompts prompts -db momos.db

clean:
	rm -rf bin momos.db
