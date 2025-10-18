
This example uses a ResourceGraphDefinition (RGD) to define an DevContainer CRD.
For more details on RGD please look at [KRO Overview](https://kro.run/docs/overview)

## Install KRO

Follow instructions to [Install KRO](https://kro.run/docs/getting-started/Installation)

```
dev/tools/install-kro
```

## Create Secret for Agent

```
kubectl create secret generic gemini-vscode-tokens --from-literal=gemini=<token>
```

## Install ResourceGraphDefinition
The administrator installs the RGD in the cluster first before the user can consume it:

```
kubectl apply -f rgd.yaml
```

Validate the RGD is installed correctly:

```
kubectl get rgd gemini-vscode-sandbox
```

Validate that the new CRD is installed correctly
```
kubectl get crd

NAME                                           CREATED AT
reviewsandboxes.custom.agents.x-k8s.io   2025-09-20T05:03:49Z  # << THIS
resourcegraphdefinitions.kro.run               2025-09-20T04:35:37Z
sandboxes.agents.x-k8s.io                      2025-09-19T22:40:05Z
```

## Create ReviewSandbox

The user creates a `ReviewSandbox` resource something like this:

```
kubectl apply -f instance.yaml
```

They can then check the status of the applied resource:

```
kubectl get reviewsandboxes
kubectl get reviewsandboxes demo -o yaml
```

Once done, the user can delete the `ReviewSandbox` instance:

```
kubectl delete reviewsandboxes demo
```

## Accesing ReviewSandbox

Verify sandbox and pod are running:

```
kubectl get sandbox devc-demo
kubectl get pod devc-demo
kubectl logs -f  devc-demo
```

Port forward the vscode server port.

```
 kubectl port-forward --address 0.0.0.0 pod/devc-demo 13337
```

Connect to the vscode-server on a browser via  http://localhost:13337 or <machine-dns>:13337

If should ask for a password.

#### Getting vscode password

In a separate terminal connect to the devcontainer pod and get the password.

```
kubectl exec  devc-demo   --  cat /root/.config/code-server/config.yaml 
```

Use the password and connect to vscode.

## User: Use gemini-cli

Gemini cli is preinstalled. Open a teminal in vscode and use Gemini cli.


--------

export ENVOY_SERVICE=$(kubectl get svc -n envoy-gateway-system --selector=gateway.envoyproxy.io/owning-gateway-namespace=default,gateway.envoyproxy.io/owning-gateway-name=codebox -o jsonpath='{.items[0].metadata.name}')

kubectl -n envoy-gateway-system port-forward --address 0.0.0.0 service/${ENVOY_SERVICE} 13337:13338 &
