
KIND_CLUSTER ?= repo-agent
REGISTRY ?= kind.local/
NAMESPACE ?= default
LOCAL_PORT ?= 13380
CLUSTER_NAME != kubectl config view --minify --output 'jsonpath={.clusters[0].name}'
INSTALL_ENVOY = $(shell if [[ ${CLUSTER_NAME} == kind* ]]; then echo "yes"; else echo "no"; fi)
GW_CLASS = $(shell if [[ ${CLUSTER_NAME} == kind* ]]; then echo "repo-agent-gatewayclass"; else echo "gke-l7-regional-external-managed"; fi)

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
  # If GITHUB_PAT is not set, we require OAuth credentials
  ifndef GITHUB_CLIENT_ID
    $(error GITHUB_PAT is not set. And GITHUB_CLIENT_ID is not set.)
  endif
  ifndef GITHUB_CLIENT_SECRET
    $(error GITHUB_PAT is not set. And GITHUB_CLIENT_SECRET is not set.)
  endif
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
	../dev/tools/fix-go-generate --working-dir ./syncer

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
	echo "INSTALL_ENVOY is set to $(INSTALL_ENVOY)"
	@if [ "$(INSTALL_ENVOY)" == "yes" ]; then \
	  	echo "Installing Envoy..."; \
		../dev/tools/install-envoy; \
	fi;
	../dev/tools/install-agent-sandbox

.PHONY: uninstall-dep-packages
uninstall-dep-packages:
	../dev/tools/install-kro --uninstall || true
	echo "INSTALL_ENVOY is set to $(INSTALL_ENVOY)"
	@if [ "$(INSTALL_ENVOY)" == "yes" ]; then \
	  	echo "UnInstalling Envoy..."; \
		../dev/tools/install-envoy --uninstall || true; \
	fi;
	../dev/tools/install-agent-sandbox --uninstall || true

.PHONY: create-kind
create-kind:
	@../dev/tools/limit-docker-log-size --check || echo "*** docker log config is missing. Adding it. Requires sudo"
	../dev/tools/limit-docker-log-size --check || sudo ../dev/tools/limit-docker-log-size
	../dev/tools/create-kind-cluster --recreate ${KIND_CLUSTER} --kubeconfig bin/KUBECONFIG

.PHONY: build-and-push-repo-agent
build-and-push-repo-agent:
	../dev/tools/push-images --image-prefix=$(REGISTRY) $(KIND_CLUSTER_ARG) --working-dir .

.PHONY: install-repo-agent
install-repo-agent:
	#  copy RGD to k8s dir so it gets deployed
	cp review-sandbox/review-sandbox-rgd.yaml k8s/
	cp issue-sandbox/issue-sandbox-rgd.yaml k8s/
	cp dev-sandbox/dev-sandbox-rgd.yaml k8s/
	../dev/tools/deploy-to-kube --image-prefix=$(REGISTRY) --working-dir . \
	--replace .spec.installationName=${CLUSTER_NAME} \
	--replace .spec.gatewayClassName=${GW_CLASS}

bin/tls.crt:
	openssl req -x509 -nodes -days 365 -newkey rsa:2048  -keyout bin/tls.key -out bin/tls.crt  -subj "/CN=repo-agent"

bin/key.json:
	echo "Generate your own Google Cloud service account key and place it in bin/key.json"
	echo "Follow instructions in ./syncer/README.md"

.PHONY: create-secrets
create-secrets: bin/tls.crt bin/key.json
	kubectl create namespace repo-agent-system || true
	@kubectl create secret -n repo-agent-system tls repo-agent-tls --cert=bin/tls.crt --key=bin/tls.key --dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret -n repo-agent-system generic gcs-credentials --from-file=key.json=./bin/key.json --dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret -n repo-agent-system generic dummy-provider-token --from-literal=dummy=testtoken --dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret -n repo-agent-system generic gemini-vscode-tokens --from-literal=gemini=${GEMINI_API_KEY} --dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret -n repo-agent-system generic github-pat --from-literal=pat="${GITHUB_PAT}" --from-literal=name="`git config --global user.name`" --from-literal=email=`git config --global user.email` --dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret -n repo-agent-system generic anthropic-api-key --from-literal=claude=${ANTHROPIC_API_KEY} --dry-run=client -o yaml | kubectl apply -f -
	# Create github-token secret for the API, optionally including OAuth credentials
	@if [ -n "${GITHUB_CLIENT_ID}" ] && [ -n "${GITHUB_CLIENT_SECRET}" ]; then \
		cmd="kubectl create secret -n repo-agent-system generic github-token --from-literal=github-client-id=${GITHUB_CLIENT_ID} --from-literal=github-client-secret=${GITHUB_CLIENT_SECRET}"; \
		if [ -n "${GITHUB_PAT}" ]; then \
			cmd="$$cmd --from-literal=token=${GITHUB_PAT}"; \
		fi; \
		$$cmd --dry-run=client -o yaml | kubectl apply -f -; \
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
	kubectl set subject clusterrolebinding dev-sandbox --serviceaccount=${NAMESPACE}:dev-sandbox
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
	ENVOY_SERVICE=$$(kubectl get svc -n envoy-gateway-system --selector=gateway.envoyproxy.io/owning-gateway-namespace=repo-agent-system,gateway.envoyproxy.io/owning-gateway-name=repo-agent-gateway -o jsonpath='{.items[0].metadata.name}') && kubectl port-forward -n envoy-gateway-system --address 0.0.0.0 service/$${ENVOY_SERVICE} $(LOCAL_PORT):80;\
	done
	# kubectl port-forward -n repo-agent-system --address 0.0.0.0 service/devc-agent-sandbox-pr-102-lb  13338:13338

# non-ephemeral (GKE) cluster install targets
.PHONY: install-repo-agent-latest
install-repo-agent-latest: install-dep-packages create-secrets
	../dev/tools/deploy-to-kube --image-prefix=ghcr.io/gke-labs/gemini-for-kubernetes-development/ \
	--image-tag=latest \
	--replace .spec.installationName=${CLUSTER_NAME} \
	--replace .spec.gatewayClassName=${GW_CLASS} \
	--working-dir . --output-file=bin/repo-agent-manifest.yaml
	kubectl apply -f bin/repo-agent-manifest.yaml

.PHONY: uninstall-repo-agent-latest
uninstall-repo-agent-latest:
	../dev/tools/deploy-to-kube --image-prefix=ghcr.io/gke-labs/gemini-for-kubernetes-development/ --image-tag=latest --working-dir . --output-file=bin/repo-agent-manifest.yaml
	kubectl delete -f bin/repo-agent-manifest.yaml || true
	${MAKE} uninstall-dep-packages
	kubectl delete crd issuesandboxes.custom.agents.x-k8s.io --wait=false || true
	kubectl patch crd issuesandboxes.custom.agents.x-k8s.io --type=json -p='[{"op":"remove", "path":"/metadata/finalizers", "value":["customresourcecleanup.apiextensions.k8s.io"]}]' || true
	kubectl delete crd reviewsandboxes.custom.agents.x-k8s.io --wait=false || true
	kubectl patch crd reviewsandboxes.custom.agents.x-k8s.io --type=json -p='[{"op":"remove", "path":"/metadata/finalizers", "value":["customresourcecleanup.apiextensions.k8s.io"]}]' || true
	kubectl delete crd devsandboxes.custom.agents.x-k8s.io --wait=false || true
	kubectl patch crd devsandboxes.custom.agents.x-k8s.io --type=json -p='[{"op":"remove", "path":"/metadata/finalizers", "value":["customresourcecleanup.apiextensions.k8s.io"]}]' || true

.PHONY: reinstall-repo-agent-latest
reinstall-repo-agent-latest: uninstall-repo-agent-latest install-repo-agent-latest
# Dev targets

.PHONY: update-repo-agent-latest
update-repo-agent-latest: install-repo-agent-latest
	kubectl rollout restart statefulset -n repo-agent-system
	kubectl rollout status  statefulset  -n repo-agent-system
	kubectl rollout restart deployment -n repo-agent-system
	kubectl rollout status  deployment  -n repo-agent-system
	@IP_ADDR=`kubectl get gateway -n repo-agent-system repo-agent-gateway -o json | jq -r ".status.addresses[0].value"`; \
	echo; echo; echo -e "\e[1;34m REPO AGENT: https://$$IP_ADDR \e[0m"; echo; echo;

.PHONY: reload-ui
reload-ui:
	make build-and-push-repo-agent
	make install-repo-agent
	kubectl scale  -n repo-agent-system deployment pr-review-ui --replicas=0
	kubectl scale  -n repo-agent-system deployment pr-review-api --replicas=0
	sleep 5
	kubectl scale  -n repo-agent-system deployment pr-review-ui --replicas=1
	kubectl scale  -n repo-agent-system deployment pr-review-api --replicas=1
	sleep 5
	make port-forward

.PHONY: clean
clean:
	rm -rf bin

##@ E2E Tests

.PHONY: test-e2e
test-e2e: ## Run e2e tests
	../dev/tools/test-e2e-chainsaw --working-dir .
