KIND_CLUSTER ?= factory
REGISTRY ?= kind.local/

# if REGISTRY is kind.local/, then we are deploying to kind
ifeq ($(REGISTRY), kind.local/)
KIND_CLUSTER_ARG=--kind-cluster-name=${KIND_CLUSTER}
else
KIND_CLUSTER_ARG=
endif

.PHONY: all
all: lint-go build push-images

.PHONY: build
build:
	@mkdir -p bin
	go build -o bin/factory .

.PHONY: lint-go
lint-go:
	../dev/tools/lint-go --working-dir ./

.PHONY: push-images
push-images:
	../dev/tools/push-images --image-prefix=$(REGISTRY) $(KIND_CLUSTER_ARG) --working-dir . --images-dir images

.PHONY: clean
clean:
	rm -rf bin
