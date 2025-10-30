
KIND_CLUSTER=repo-agent
REGISTRY ?= kind.local/

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
all: check-prereqs generate lint-go test-unit create-kind install-packages install-sandbox-operator create-secrets install-repo-agent create-instance

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

.PHONY: install-packages
install-packages:
	../dev/tools/install-kro
	../dev/tools/install-envoy

.PHONY: create-kind
create-kind:
	../dev/tools/create-kind-cluster --recreate ${KIND_CLUSTER} --kubeconfig bin/KUBECONFIG

.PHONY: install-sandbox-operator
install-sandbox-operator:
	# Install Sandbox Operator 
	# have to do from source until we have a release
	mkdir /tmp/sandbox-operator || true
	ls -l /tmp/sandbox-operator/agent-sandbox || git clone https://github.com/kubernetes-sigs/agent-sandbox.git /tmp/sandbox-operator/agent-sandbox
	cd /tmp/sandbox-operator/agent-sandbox && git pull
	cd /tmp/sandbox-operator/agent-sandbox && dev/tools/push-images --image-prefix=$(REGISTRY) $(KIND_CLUSTER_ARG)
	cd /tmp/sandbox-operator/agent-sandbox && dev/tools/deploy-to-kube --image-prefix=$(REGISTRY)
	rm -fr /tmp/sandbox-operator

.PHONY: install-repo-agent
install-repo-agent:
	kubectl apply -f k8s/namespace.yaml
	# Install
	../dev/tools/push-images --image-prefix=$(REGISTRY) $(KIND_CLUSTER_ARG) --working-dir .
	#  copy RGD to k8s dir so it gets deployed
	cp review-sandbox/review-sandbox-rgd.yaml k8s/
	cp issue-sandbox/issue-sandbox-rgd.yaml k8s/
	../dev/tools/deploy-to-kube --image-prefix=$(REGISTRY) --working-dir .

.PHONY: create-secrets
create-secrets:
	kubectl create namespace repo-agent-system || true
	@kubectl create secret -n repo-agent-system generic gemini-vscode-tokens --from-literal=gemini=${GEMINI_API_KEY} || true
	@kubectl create secret -n repo-agent-system generic github-pat --from-literal=pat=${GITHUB_PAT} --from-literal=name="`git config --global user.name`" --from-literal=email=`git config --global user.email`|| true

bin/configdir-cli: configdir/
	../dev/tools/build-go-binaries --working-dir .

.PHONY: create-instance
create-instance: ./bin/configdir-cli
	kubectl apply -f examples/agent-sandbox-repowatch.yaml
	kubectl apply -f examples/kcc-configmap-devcontainer.yaml # custom devcontainer for KCC
	kubectl apply -f examples/kcc-configdir.yaml              # custom .gemini for KCC
	kubectl apply -f examples/kcc-repowatch.yaml              # repowatch for KCC
	kubectl apply -f examples/kro-configmap-devcontainer.yaml # custom devcontainer for KRO
	kubectl apply -f examples/kro-repowatch.yaml               # repowatch for KRO
	sleep 5
	kubectl get reviewsandboxes -A
	kubectl get issuesandboxes -A
	# bin/configdir-cli --sync-to-cluster  --directory ./configdir/test-sample/cc/.gemini --namespace repo-agent-system --name gemini-cc

.PHONY: delete-instance
delete-instance:
	kubectl delete reviewsandbox demo

.PHONY: port-forward
port-forward:
	while true; do \
	ENVOY_SERVICE=$$(kubectl get svc -n envoy-gateway-system --selector=gateway.envoyproxy.io/owning-gateway-namespace=repo-agent-system,gateway.envoyproxy.io/owning-gateway-name=repo-agent-gateway -o jsonpath='{.items[0].metadata.name}') && kubectl port-forward -n envoy-gateway-system --address 0.0.0.0 service/$${ENVOY_SERVICE} 13380:13380;\
	done
	# kubectl port-forward -n repo-agent-system --address 0.0.0.0 service/devc-agent-sandbox-pr-102-lb  13338:13338
