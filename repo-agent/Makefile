
KIND_CLUSTER ?= repo-agent
REGISTRY ?= kind.local/
NAMESPACE ?= default

# if REGISTRY is kind.local/, then we are deploying to kind
ifeq ($(REGISTRY), kind.local/)
KIND_CLUSTER_ARG=--kind-cluster-name=${KIND_CLUSTER}
else
KIND_CLUSTER_ARG=
endif

# Check pre-reqs
ifndef GEMINI_API_KEY
$(error GEMINI_API_KEY is not set. Please set it before running make.)
endif

ifndef GITHUB_PAT
$(error GITHUB_PAT is not set. Please set it before running make.)
endif

ifndef ANTHROPIC_API_KEY
$(warning ANTHROPIC_API_KEY is not set. Claude provider will not work.)
endif

# Optional: Check for OAuth credentials to automate API secret creation
ifndef GITHUB_CLIENT_ID
$(warning GITHUB_CLIENT_ID is not set. API oauth will need manual setup in k8s/api-secret.yaml or via kubectl)
endif
ifndef GITHUB_CLIENT_SECRET
$(warning GITHUB_CLIENT_SECRET is not set. API oauth will need manual setup in k8s/api-secret.yaml or via kubectl)
endif

GIT_USER_NAME := $(strip $(shell git config --global user.name))
ifeq ($(GIT_USER_NAME),)
    # The error function prints an error message and stops the build process.
    $(error "git config --global user.name" is not set. Please configure it with 'git config --global user.name "Your Name"'. )
endif
GIT_USER_EMAIL := $(strip $(shell git config --global user.email))
ifeq ($(GIT_USER_EMAIL),)
    # The error function prints an error message and stops the build process.
    $(error "git config --global user.email" is not set. Please configure it with 'git config --global user.email "email@domain.com"'. )
endif

.PHONY: all
all: check-prereqs generate lint-go test-unit create-kind install-dep-packages create-secrets build-and-push-repo-agent install-repo-agent #create-instance

.PHONY: install
install: check-prereqs install-dep-packages create-secrets install-repo-agent

.PHONY: check-prereqs
check-prereqs:
	@echo "Checking for prerequisites..."
	@command -v kind >/dev/null 2>&1 || { echo >&2 "kind not found. Please install it. https://kind.sigs.k8s.io/docs/user/quick-start/#installation"; exit 1; }
	@command -v kubectl >/dev/null 2>&1 || { echo >&2 "kubectl not found. Please install it. https://kubernetes.io/docs/tasks/tools/install-kubectl/"; exit 1; }
	@command -v helm >/dev/null 2>&1 || { echo >&2 "helm not found. Please install it. https://helm.sh/docs/intro/install/"; exit 1; }
	@echo "All prerequisites are installed."

.PHONY: generate
generate:
	../dev/tools/fix-go-generate --working-dir ./repowatch
	../dev/tools/fix-go-generate --working-dir ./configdir

.PHONY: build
build: generate
	../dev/tools/build-go-binaries --working-dir ./

.PHONY: test-unit
test-unit:
	../dev/tools/test-unit --working-dir ./

.PHONY: lint-go
lint-go:
	../dev/tools/lint-go --working-dir ./

.PHONY: install-dep-packages
install-dep-packages:
	../dev/tools/install-kro
	../dev/tools/install-envoy
	../dev/tools/install-agent-sandbox

.PHONY: create-kind
create-kind:
	../dev/tools/create-kind-cluster --recreate ${KIND_CLUSTER} --kubeconfig bin/KUBECONFIG

.PHONY: build-and-push-repo-agent
build-and-push-repo-agent:
	../dev/tools/push-images --image-prefix=$(REGISTRY) $(KIND_CLUSTER_ARG) --working-dir .

.PHONY: install-repo-agent
install-repo-agent:
	#  copy RGD to k8s dir so it gets deployed
	cp review-sandbox/review-sandbox-rgd.yaml k8s/
	cp issue-sandbox/issue-sandbox-rgd.yaml k8s/
	../dev/tools/deploy-to-kube --image-prefix=$(REGISTRY) --working-dir .

.PHONY: create-secrets
create-secrets:
	kubectl create namespace repo-agent-system || true
	@kubectl create secret -n repo-agent-system generic gemini-vscode-tokens --from-literal=gemini=${GEMINI_API_KEY} --dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret -n repo-agent-system generic github-pat --from-literal=pat=${GITHUB_PAT} --from-literal=name="`git config --global user.name`" --from-literal=email=`git config --global user.email` --dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret -n repo-agent-system generic anthropic-api-key --from-literal=claude=${ANTHROPIC_API_KEY} --dry-run=client -o yaml | kubectl apply -f -
	# Create github-token secret for the API, optionally including OAuth credentials
	@if [ -n "${GITHUB_CLIENT_ID}" ] && [ -n "${GITHUB_CLIENT_SECRET}" ]; then \
		kubectl create secret -n repo-agent-system generic github-token \
			--from-literal=token=${GITHUB_PAT} \
			--from-literal=github-client-id=${GITHUB_CLIENT_ID} \
			--from-literal=github-client-secret=${GITHUB_CLIENT_SECRET} \
			--dry-run=client -o yaml | kubectl apply -f -; \
	else \
		kubectl create secret -n repo-agent-system generic github-token \
			--from-literal=token=${GITHUB_PAT} \
			--dry-run=client -o yaml | kubectl apply -f -; \
	fi

bin/configdir-cli: configdir/
	../dev/tools/build-go-binaries --working-dir .

.PHONY: apply-common-for-examples
apply-common-for-examples:
	# TODO(barney-s): is there a better way to handle SA ?
	kubectl apply -f examples/sandbox-rbac.yaml -n $(NAMESPACE)
	kubectl set subject clusterrolebinding review-sandbox --serviceaccount=${NAMESPACE}:review-sandbox
	kubectl set subject clusterrolebinding issue-sandbox --serviceaccount=${NAMESPACE}:issue-sandbox
	# end of SA
	kubectl apply -f examples/go-configmap-devcontainer.yaml -n $(NAMESPACE)

.PHONY: apply-examples
apply-examples: apply-common-for-examples
	kubectl apply -f examples/gkelabs-geminifork8s-repowatch.yaml -n $(NAMESPACE)
	kubectl apply -f examples/agent-sandbox-repowatch.yaml -n $(NAMESPACE)
	kubectl apply -f examples/kcc-configdir.yaml -n $(NAMESPACE)
	kubectl apply -f examples/kcc-repowatch.yaml -n $(NAMESPACE)
	kubectl apply -f examples/kro-repowatch.yaml -n $(NAMESPACE)
	kubectl apply -f examples/k8s-configdir.yaml -n $(NAMESPACE)
	kubectl apply -f examples/k8s-repowatch.yaml -n $(NAMESPACE)
	sleep 5
	kubectl get reviewsandboxes -A
	kubectl get issuesandboxes -A

.PHONY: delete-instance
delete-instance:
	kubectl delete reviewsandbox demo

.PHONY: port-forward
port-forward:
	while true; do \
	ENVOY_SERVICE=$$(kubectl get svc -n envoy-gateway-system --selector=gateway.envoyproxy.io/owning-gateway-namespace=repo-agent-system,gateway.envoyproxy.io/owning-gateway-name=repo-agent-gateway -o jsonpath='{.items[0].metadata.name}') && kubectl port-forward -n envoy-gateway-system --address 0.0.0.0 service/$${ENVOY_SERVICE} 13380:13380;\
	done
	# kubectl port-forward -n repo-agent-system --address 0.0.0.0 service/devc-agent-sandbox-pr-102-lb  13338:13338

# Dev targets

.PHONY: reload-ui
reload-ui:
	make build-and-push-repo-agent
	make install-repo-agent
	kubectl scale  -n repo-agent-system deployment pr-review-ui --replicas=0
	sleep 5
	kubectl scale  -n repo-agent-system deployment pr-review-ui --replicas=1
	sleep 5
	make port-forward

.PHONY: clean
clean:
	rm -rf bin

##@ E2E Tests

.PHONY: test-e2e
test-e2e: ## Run e2e tests
	../dev/tools/test-e2e-chainsaw --working-dir .
