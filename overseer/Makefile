KIND_CLUSTER ?= overseer-agent
REGISTRY ?= kind.local/

# if REGISTRY is kind.local/, then we are deploying to kind
ifeq ($(REGISTRY), kind.local/)
KIND_CLUSTER_ARG=--kind-cluster-name=${KIND_CLUSTER}
else
KIND_CLUSTER_ARG=
endif

.PHONY: all
all: check-prereqs generate lint-go test-unit create-kind install-dep-packages create-secrets push-images install

.PHONY: check-prereqs
check-prereqs:
	@echo "Checking for prerequisites..."
	@command -v kind >/dev/null 2>&1 || { echo >&2 "kind not found. Please install it. https://kind.sigs.k8s.io/docs/user/quick-start/#installation"; exit 1; }
	@command -v kubectl >/dev/null 2>&1 || { echo >&2 "kubectl not found. Please install it. https://kubernetes.io/docs/tasks/tools/install-kubectl/"; exit 1; }
	@command -v helm >/dev/null 2>&1 || { echo >&2 "helm not found. Please install it. https://helm.sh/docs/intro/install/"; exit 1; }
	@echo "All prerequisites are installed."

.PHONY: generate
generate:
	../dev/tools/fix-go-generate --working-dir ./pkg/api

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

bin/github-portal-ca.crt:
	@mkdir -p bin
	openssl req -x509 -nodes -days 365 -newkey rsa:2048 -keyout bin/github-portal-ca.key -out bin/github-portal-ca.crt -subj "/CN=github-portal-ca"

.PHONY: create-secrets
create-secrets: bin/github-portal-ca.crt
	@if [ -z "${GEMINI_API_KEY}" ]; then \
		echo "Error: GEMINI_API_KEY is not set." >&2; \
		exit 1; \
	fi
	@if [ -z "${ROBOT1_GH_PAT}" ]; then \
		echo "Error: ROBOT1_GH_PAT is not set." >&2; \
		exit 1; \
	fi
	@if [ -z "${ROBOT1_GH_USERID}" ]; then \
		echo "Error: ROBOT1_GH_USERID is not set." >&2; \
		exit 1; \
	fi
	kubectl create namespace overseer-system || true
	@kubectl create secret -n overseer-system generic gemini-vscode-tokens --from-literal=gemini=${GEMINI_API_KEY} --dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret -n overseer-system generic ${ROBOT1_GH_USERID} \
		--from-literal=pat="${ROBOT1_GH_PAT}" \
		--from-literal=name="${ROBOT1_GH_NAME}" \
		--from-literal=userid="${ROBOT1_GH_USERID}" \
		--from-literal=email="${ROBOT1_GH_EMAIL}" \
		--dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret generic github-portal-ca -n overseer-system --from-file=tls.crt=bin/github-portal-ca.crt --from-file=tls.key=bin/github-portal-ca.key --dry-run=client -o yaml | kubectl apply -f -

.PHONY: install-dep-packages
install-dep-packages:
	../dev/tools/install-agent-sandbox
	kubectl apply -f ../repo-agent/k8s/crds/ || true
	kubectl create namespace overseer-system || true

.PHONY: uninstall-dep-packages
uninstall-dep-packages:
	../dev/tools/install-agent-sandbox --uninstall || true
	kubectl delete -f deps/github-portal.yaml || true

.PHONY: create-kind
create-kind:
	@../dev/tools/limit-docker-log-size --check || echo "*** docker log config is missing. Adding it. Requires sudo"
	../dev/tools/limit-docker-log-size --check || sudo ../dev/tools/limit-docker-log-size || true
	../dev/tools/create-kind-cluster --recreate ${KIND_CLUSTER} --kubeconfig bin/KUBECONFIG

.PHONY: install
install:
	../dev/tools/deploy-to-kube --image-prefix=$(REGISTRY) --working-dir .
	kubectl apply -f deps/github-portal.yaml

IMAGE_TAG ?= latest
SKIP_PREQS ?= false

# non-ephemeral (GKE) cluster install targets
.PHONY: install-latest
install-latest:
	@if [ "$(SKIP_PREQS)" != "true" ]; then \
		$(MAKE) install-dep-packages create-secrets; \
	fi
	@mkdir -p bin
	../dev/tools/deploy-to-kube \
	--working-dir=. \
	--image-prefix=ghcr.io/gke-labs/gemini-for-kubernetes-development/ \
	--image-tag=$(IMAGE_TAG) \
	--output-file=$(CURDIR)/bin/overseer-manifest.yaml
	kubectl apply -f bin/overseer-manifest.yaml

.PHONY: uninstall-latest
uninstall-latest:
	../dev/tools/deploy-to-kube \
	--working-dir=. \
	--image-prefix=ghcr.io/gke-labs/gemini-for-kubernetes-development/ \
	--image-tag=$(IMAGE_TAG) \
	--output-file=$(CURDIR)/bin/overseer-manifest.yaml
	kubectl delete -f bin/overseer-manifest.yaml || true
	@if [ "$(SKIP_PREQS)" != "true" ]; then \
	  ${MAKE} uninstall-dep-packages; \
	  kubectl delete crd sandboxes.agents.x-k8s.io --wait=false || true
	  kubectl patch crd sandboxes.agents.x-k8s.io --type=json -p='[{"op":"remove", "path":"/metadata/finalizers", "value":["customresourcecleanup.apiextensions.k8s.io"]}]' || true
	fi

.PHONY: update-latest
update-latest: install-latest
	kubectl rollout restart statefulset -n overseer-system || true
	kubectl rollout status  statefulset  -n overseer-system || true
	kubectl rollout restart deployment -n overseer-system
	kubectl rollout status  deployment  -n overseer-system


.PHONY: push-images-overseer
push-images-overseer:
	../dev/tools/push-images --image-prefix=$(REGISTRY) $(KIND_CLUSTER_ARG) --working-dir .. --images-dir images

.PHONY: push-images
push-images: push-images-overseer
	../dev/tools/push-images --image-prefix=$(REGISTRY) $(KIND_CLUSTER_ARG) \
	--working-dir .. \
	--images-dir ../repo-agent/images \
	--only repo-sandbox \
	--only configdir-cli

.PHONY: clean
clean:
	rm -rf bin
