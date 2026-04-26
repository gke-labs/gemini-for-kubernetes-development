
KIND_CLUSTER ?= repo-agent
REGISTRY ?= kind.local/
NAMESPACE ?= default
LOCAL_PORT ?= 13380
CLUSTER_NAME ?= $(shell kubectl config view --minify --output 'jsonpath={.clusters[0].name}')
INSTALL_ENVOY ?= $(if $(filter kind%,$(CLUSTER_NAME)),yes,no)
GW_CLASS ?= $(if $(filter kind%,$(CLUSTER_NAME)),repo-agent-gatewayclass,gke-l7-regional-external-managed)
LLM_PROVIDER ?= gemini-cli
DEFAULT_LLM_PROVIDER = $(if $(filter kind%,$(CLUSTER_NAME)),$(LLM_PROVIDER),gemini-cli)

# if REGISTRY is kind.local/, then we are deploying to kind
ifeq ($(REGISTRY), kind.local/)
KIND_CLUSTER_ARG=--kind-cluster-name=${KIND_CLUSTER}
else
KIND_CLUSTER_ARG=
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
	@command -v node >/dev/null 2>&1 || { echo >&2 "node not found. Please install it. https://nodejs.org/"; exit 1; }
	@command -v npm >/dev/null 2>&1 || { echo >&2 "npm not found. Please install it. https://docs.npmjs.com/downloading-and-installing-node-js-and-npm"; exit 1; }
	@echo "All prerequisites are installed."

.PHONY: generate
generate:
	../dev/tools/fix-go-generate --working-dir ./api
	../dev/tools/fix-go-generate --working-dir ./pkg/controllers

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

.PHONY: install-dep-packages
install-dep-packages:
	../dev/tools/install-kro
	echo "INSTALL_ENVOY is set to $(INSTALL_ENVOY)"
	@if [ "$(INSTALL_ENVOY)" = "yes" ]; then \
	  	echo "Installing Envoy..."; \
		../dev/tools/install-envoy; \
	fi;
	../dev/tools/install-agent-sandbox

.PHONY: uninstall-dep-packages
uninstall-dep-packages:
	../dev/tools/install-kro --uninstall || true
	echo "INSTALL_ENVOY is set to $(INSTALL_ENVOY)"
	@if [ "$(INSTALL_ENVOY)" = "yes" ]; then \
	  	echo "UnInstalling Envoy..."; \
		../dev/tools/install-envoy --uninstall || true; \
	fi;
	../dev/tools/install-agent-sandbox --uninstall || true

.PHONY: create-kind
create-kind:
	@../dev/tools/limit-docker-log-size --check || echo "*** docker log config is missing. Adding it. Requires sudo"
	../dev/tools/limit-docker-log-size --check || sudo ../dev/tools/limit-docker-log-size || true
	../dev/tools/create-kind-cluster --recreate ${KIND_CLUSTER} --kubeconfig bin/KUBECONFIG

# --skip generic-golang for faster builds
# github presubmits builds and pushed this image separately
.PHONY: build-and-push-repo-agent
build-and-push-repo-agent:
	../dev/tools/push-images --image-prefix=$(REGISTRY) $(KIND_CLUSTER_ARG) \
	--working-dir .. \
	--images-dir images \
	--skip generic-golang \
	--skip dind-golang \
	--skip github-mcp-server

.PHONY: install-repo-agent
install-repo-agent:
	# Using crd yaml directly due to KRO bug with status fields not being created
	# cp cmd/repo-sandbox/sandbox-task-rgd.yaml k8s/
	../dev/tools/deploy-to-kube --image-prefix=$(REGISTRY) --working-dir . \
	--replace .spec.installationName=${CLUSTER_NAME} \
	--replace .spec.gatewayClassName=${GW_CLASS} \
	--replace .spec.versions[].schema.openAPIV3Schema.properties.spec.properties.dev.properties.llm.properties.provider.default=${DEFAULT_LLM_PROVIDER} \
	--replace .spec.versions[].schema.openAPIV3Schema.properties.spec.properties.review.properties.llm.properties.provider.default=${DEFAULT_LLM_PROVIDER} \
	--replace .spec.versions[].schema.openAPIV3Schema.properties.spec.properties.issueHandlers.items.properties.llm.properties.provider.default=${DEFAULT_LLM_PROVIDER}

.PHONY: install-overseer
install-overseer:
	(cd ../overseer && KIND_CLUSTER=${KIND_CLUSTER} $(MAKE) push-images-overseer install)

bin/tls.crt:
	openssl req -x509 -nodes -days 365 -newkey rsa:2048  -keyout bin/tls.key -out bin/tls.crt  -subj "/CN=repo-agent"

bin/key.json:
	@echo "Warning: bin/key.json not found. This is optional for local KinD development but required for GKE/GCS sync."
	@echo "To generate: Follow instructions in ./syncer/README.md"

.PHONY: create-secrets
create-secrets: bin/tls.crt bin/key.json
	@if [ -z "${GITHUB_CLIENT_ID}" ] && [ -z "${GEMINI_API_KEY}" ]; then \
		echo "Error: GEMINI_API_KEY is not set. It is required for single-user mode. Please set it or GITHUB_CLIENT_ID before running make." >&2; \
		exit 1; \
	fi
	@if [ -z "${GITHUB_PAT}" ]; then \
		if [ -z "${GITHUB_CLIENT_ID}" ]; then \
			echo "Error: GITHUB_PAT is not set. And GITHUB_CLIENT_ID is not set." >&2; \
			exit 1; \
		fi; \
		if [ -z "${GITHUB_CLIENT_SECRET}" ]; then \
			echo "Error: GITHUB_PAT is not set. And GITHUB_CLIENT_SECRET is not set." >&2; \
			exit 1; \
		fi; \
	fi
	@if [ -z "${ANTHROPIC_API_KEY}" ]; then \
		echo "Warning: ANTHROPIC_API_KEY is not set. Claude provider will not work."; \
	fi
	@if [ -z "${GITHUB_CLIENT_ID}" ]; then \
		echo "Warning: GITHUB_CLIENT_ID is not set. API oauth will need manual setup in k8s/api-secret.yaml or via kubectl"; \
	fi
	@if [ -z "${GITHUB_CLIENT_SECRET}" ]; then \
		echo "Warning: GITHUB_CLIENT_SECRET is not set. API oauth will need manual setup in k8s/api-secret.yaml or via kubectl"; \
	fi
	@if [ -z "$$(git config --global user.name)" ]; then \
		echo "Error: \"git config --global user.name\" is not set. Please configure it with 'git config --global user.name \"Your Name\"'." >&2; \
		exit 1; \
	fi
	@if [ -z "$$(git config --global user.email)" ]; then \
		echo "Error: \"git config --global user.email\" is not set. Please configure it with 'git config --global user.email \"email@domain.com\"'." >&2; \
		exit 1; \
	fi
	kubectl create namespace repo-agent-system || true
	@kubectl create secret -n repo-agent-system tls repo-agent-tls --cert=bin/tls.crt --key=bin/tls.key --dry-run=client -o yaml | kubectl apply -f -
	@if [ -f ./bin/key.json ]; then \
		kubectl create secret -n repo-agent-system generic gcs-credentials --from-file=key.json=./bin/key.json --dry-run=client -o yaml | kubectl apply -f -; \
	fi
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
	# Create robot account secret if ROBOT1_GH_PAT is set
	@if [ -n "${ROBOT1_GH_PAT}" ]; then \
		kubectl create secret -n repo-agent-system generic ${ROBOT1_GH_USERID} \
			--from-literal=pat="${ROBOT1_GH_PAT}" \
			--from-literal=name="${ROBOT1_GH_NAME}" \
			--from-literal=userid="${ROBOT1_GH_USERID}" \
			--from-literal=email="${ROBOT1_GH_EMAIL}" \
			--dry-run=client -o yaml | kubectl apply -f -; \
	fi

bin/configdir-cli: configdir/
	../dev/tools/build-go-binaries --working-dir .

.PHONY: apply-common-for-examples
apply-common-for-examples:
	# TODO(barney-s): is there a better way to handle SA ?
	kubectl apply -f examples/sandbox-rbac.yaml -n $(NAMESPACE)
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
	kubectl get sandboxes -A

.PHONY: delete-instance
delete-instance:
	kubectl delete sandbox demo

.PHONY: port-forward
port-forward:
	while true; do \
	ENVOY_SERVICE=$$(kubectl get svc -n envoy-gateway-system --selector=gateway.envoyproxy.io/owning-gateway-namespace=repo-agent-system,gateway.envoyproxy.io/owning-gateway-name=repo-agent-gateway -o jsonpath='{.items[0].metadata.name}') && kubectl port-forward -n envoy-gateway-system --address 0.0.0.0 service/$${ENVOY_SERVICE} $(LOCAL_PORT):80;\
	done
	# kubectl port-forward -n repo-agent-system --address 0.0.0.0 service/agent-sandbox-pr-102-lb  13338:13338

IMAGE_TAG ?= latest
SKIP_PREQS ?= false

# non-ephemeral (GKE) cluster install targets
.PHONY: install-repo-agent-latest
install-repo-agent-latest:
	@if [ "$(SKIP_PREQS)" != "true" ]; then \
		$(MAKE) install-dep-packages create-secrets; \
	fi
	@mkdir -p bin
	../dev/tools/deploy-to-kube --image-prefix=ghcr.io/gke-labs/gemini-for-kubernetes-development/ \
	--image-tag=$(IMAGE_TAG) \
	--replace .spec.installationName=${CLUSTER_NAME} \
	--replace .spec.gatewayClassName=${GW_CLASS} \
	--working-dir . --output-file=bin/repo-agent-manifest.yaml
	kubectl apply -f bin/repo-agent-manifest.yaml

.PHONY: uninstall-repo-agent-latest
uninstall-repo-agent-latest:
	../dev/tools/deploy-to-kube --image-prefix=ghcr.io/gke-labs/gemini-for-kubernetes-development/ --image-tag=$(IMAGE_TAG) --working-dir . --output-file=bin/repo-agent-manifest.yaml
	kubectl delete -f bin/repo-agent-manifest.yaml || true
	${MAKE} uninstall-dep-packages
	kubectl delete crd sandboxes.agents.x-k8s.io --wait=false || true
	kubectl patch crd sandboxes.agents.x-k8s.io --type=json -p='[{"op":"remove", "path":"/metadata/finalizers", "value":["customresourcecleanup.apiextensions.k8s.io"]}]' || true

.PHONY: reinstall-repo-agent-latest
reinstall-repo-agent-latest: uninstall-repo-agent-latest install-repo-agent-latest
# Dev targets

.PHONY: update-repo-agent-latest
update-repo-agent-latest: install-repo-agent-latest
	kubectl rollout restart statefulset -n repo-agent-system
	kubectl rollout status  statefulset  -n repo-agent-system
	kubectl rollout restart deployment -n repo-agent-system
	kubectl rollout status  deployment  -n repo-agent-system

.PHONY: show-url
show-url:
	@IP_ADDR=`kubectl get gateway -n repo-agent-system repo-agent-gateway -o json | jq -r ".status.addresses[0].value"`; \
	echo; echo; echo -e "\e[1;34m REPO AGENT: https://$$IP_ADDR \e[0m"; echo; echo;


.PHONY: reload-ui
reload-ui:
	../dev/tools/push-images --image-prefix=$(REGISTRY) $(KIND_CLUSTER_ARG) --working-dir .. --images-dir images \
	--only review-api \
	--only review-ui
	$(MAKE) install-repo-agent
	kubectl rollout restart statefulset -n repo-agent-system
	kubectl rollout status  statefulset  -n repo-agent-system
	kubectl rollout restart deployment -n repo-agent-system
	kubectl rollout status  deployment  -n repo-agent-system
	sleep 5
	make port-forward

.PHONY: reload
reload: build-and-push-repo-agent install-repo-agent
	kubectl rollout restart statefulset -n repo-agent-system
	kubectl rollout status  statefulset  -n repo-agent-system
	kubectl rollout restart deployment -n repo-agent-system
	kubectl rollout status  deployment  -n repo-agent-system
	sleep 5
	make port-forward

.PHONY: clean
clean:
	rm -rf bin

##@ E2E Tests

.PHONY: test-e2e
test-e2e: ## Run e2e tests
	../dev/tools/test-e2e-chainsaw --working-dir .
