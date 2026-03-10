KIND_CLUSTER ?= repo-agent
REGISTRY ?= kind.local/

# if REGISTRY is kind.local/, then we are deploying to kind
ifeq ($(REGISTRY), kind.local/)
KIND_CLUSTER_ARG=--kind-cluster-name=${KIND_CLUSTER}
else
KIND_CLUSTER_ARG=
endif

.PHONY: all
all: lint-go test-unit build

.PHONY: generate
generate:
	../dev/tools/fix-go-generate --working-dir ./api

.PHONY: build
build: generate
	../dev/tools/build-go-binaries --working-dir ./

.PHONY: test-unit
test-unit:
	../dev/tools/test-unit --working-dir ./cmd/
	../dev/tools/test-unit --working-dir ./pkg/

.PHONY: lint-go
lint-go:
	../dev/tools/lint-go --working-dir ./

.PHONY: push-images
push-images:
	../dev/tools/push-images --image-prefix=$(REGISTRY) $(KIND_CLUSTER_ARG) --working-dir .. --images-dir images

.PHONY: deploy
deploy:
	../dev/tools/deploy-to-kube --image-prefix=$(REGISTRY) --working-dir . $(DEPLOY_ARGS)

.PHONY: clean
clean:
	rm -rf bin
