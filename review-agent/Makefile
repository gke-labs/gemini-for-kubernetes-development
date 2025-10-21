
KIND_CLUSTER=review-agent

.PHONY: all
all: generate lint-go test-unit create-kind install-packages install-sandbox-operator create-secrets install-review-agent create-instance

.PHONY: generate
generate:
	../dev/tools/fix-go-generate --working-dir ./repowatch
	../dev/tools/fix-go-generate --working-dir ./configdir

.PHONY: build
build:
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
	cd /tmp/sandbox-operator/agent-sandbox && dev/tools/push-images --image-prefix=kind.local/ --kind-cluster-name=${KIND_CLUSTER}
	cd /tmp/sandbox-operator/agent-sandbox && dev/tools/deploy-to-kube --image-prefix=kind.local/
	rm -fr /tmp/sandbox-operator

.PHONY: install-review-agent
install-review-agent:
	kubectl apply -f k8s/namespace.yaml
	# Install
	../dev/tools/push-images --image-prefix=kind.local/ --kind-cluster-name=${KIND_CLUSTER} --working-dir .
	#  copy RGD to k8s dir so it gets deployed
	cp review-sandbox/review-sandbox-rgd.yaml k8s/review-sandbox-rgd.yaml
	../dev/tools/deploy-to-kube --image-prefix=kind.local/ --working-dir .

.PHONY: create-secrets
create-secrets:
	kubectl create namespace review-agent-system || true
	@kubectl create secret -n review-agent-system generic gemini-vscode-tokens --from-literal=gemini=${GEMINI_API_KEY} || true
	@kubectl create secret -n review-agent-system generic github-pat --from-literal=pat=${GITHUB_PAT} || true

bin/configdir-cli: configdir/
	../dev/tools/build-go-binaries --working-dir .

.PHONY: create-instance
create-instance: ./bin/configdir-cli
	bin/configdir-cli --sync-to-cluster  --directory ./configdir/test-sample/cc/.gemini --namespace review-agent-system --name gemini-cc
	# create repowatch instance
	kubectl apply -f examples/repowatch.yaml
	sleep 5
	kubectl get reviewsandboxes -A

.PHONY: delete-instance
delete-instance:
	kubectl delete reviewsandbox demo

.PHONY: port-forward
port-forward:
	while true; do \
	ENVOY_SERVICE=$$(kubectl get svc -n envoy-gateway-system --selector=gateway.envoyproxy.io/owning-gateway-namespace=review-agent-system,gateway.envoyproxy.io/owning-gateway-name=review-agent-gateway -o jsonpath='{.items[0].metadata.name}') && kubectl port-forward -n envoy-gateway-system --address 0.0.0.0 service/$${ENVOY_SERVICE} 13380:13380;\
	done
	# kubectl port-forward -n review-agent-system --address 0.0.0.0 service/devc-agent-sandbox-pr-102-lb  13338:13338
