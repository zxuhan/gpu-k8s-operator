# AKS scaffolding

Bicep + GitHub Actions that spin up a small AKS cluster and push the
gwb-operator image there. Opinionated for student subscriptions — B2s
nodes, Basic ACR, system-assigned managed identity — so the monthly
cost stays near the $30 free credit.

## One-time setup

```sh
az login
az account set --subscription <your-sub>

# Create the resource group that holds the cluster + registry.
az group create -n gwb-operator-rg -l eastus

# Provision with the example parameters (edit as needed).
az deployment group create \
  --resource-group gwb-operator-rg \
  --template-file deploy/aks/main.bicep \
  --parameters @deploy/aks/parameters.example.json

# Grab kubeconfig + ACR login.
az aks get-credentials -g gwb-operator-rg -n gwb-aks
az acr login --name <registryName from deployment output>
```

## GitHub Actions

`.github/workflows/aks-deploy.yml` builds the operator image on every
push to `main`, pushes it to the ACR, and installs the Helm chart via
`azure/setup-helm` + `helm upgrade --install`. The workflow is gated
on `workflow_dispatch` by default — turn on the `push:` trigger once
you're happy with it.

### Required repo secrets

| Secret | Value |
|---|---|
| `AZURE_CREDENTIALS` | `az ad sp create-for-rbac --sdk-auth` JSON, scoped to the RG |
| `AZURE_RESOURCE_GROUP` | e.g. `gwb-operator-rg` |
| `AKS_CLUSTER_NAME` | e.g. `gwb-aks` |
| `ACR_NAME` | the generated ACR name (from deployment output `registryName`) |

Do NOT commit these. See [docs/limitations.md](../../docs/limitations.md)
for why the bicep sets `adminUserEnabled: false` — AcrPull is granted to
the cluster's kubelet identity directly, so the workflow never sees a
registry password.

## What you get

- 1× AKS cluster, Kubernetes 1.31, 2× B2s nodes, Azure CNI overlay.
- 1× ACR (Basic SKU), `adminUserEnabled=false`.
- AcrPull role assignment from ACR to AKS kubelet identity.

What you don't get, and why:

- **No GPU node pool.** Student subs rarely have GPU quota; add one
  with `az aks nodepool add --node-vm-size Standard_NC6s_v3 ...` once
  you have quota, and the operator's control loop will pick up
  `nvidia.com/gpu` without a code change.
- **No monitoring addon.** Azure Monitor for Containers is off
  (~$1/GB ingested). Enable it via the portal or set
  `addonProfiles.omsagent.enabled` in the bicep.
- **No private cluster.** The API server is public; ACLs are at the
  subscription level. Change `apiServerAccessProfile` if you need
  a private control plane.
- **AAD RBAC disabled.** Local `kubectl` auth works out of the box;
  flip `disableLocalAccounts: true` once you wire AAD.
