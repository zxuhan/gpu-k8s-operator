// Top-level template for a small, opinionated AKS deployment suitable
// for a student subscription. Deliberately minimal: one user node pool
// with Standard_B2s nodes (the smallest burstable SKU that still fits
// the operator + cert-manager + kube-prometheus-stack footprint), no
// attached GPU pool, and a tiny ACR tied to the same resource group
// via `az acr login --name` without any separate service principal.
//
// Usage:
//   az group create -n gwb-operator-rg -l eastus
//   az deployment group create \
//     --resource-group gwb-operator-rg \
//     --template-file deploy/aks/main.bicep \
//     --parameters environmentPrefix=gwb location=eastus
//
// `environmentPrefix` exists so a developer with multiple student subs
// can deploy the same template twice without name collisions on the
// ACR (which must be globally unique).

@description('Short prefix applied to every resource; drives globally unique names.')
@minLength(3)
@maxLength(12)
param environmentPrefix string = 'gwb'

@description('Azure region. Student subscriptions usually default to eastus / westus2.')
param location string = resourceGroup().location

@description('Kubernetes version. Pin so upgrade surprises are explicit in git.')
param kubernetesVersion string = '1.31.2'

@description('Number of nodes in the default pool. 2 is the minimum that lets cert-manager spread.')
@minValue(1)
@maxValue(5)
param nodeCount int = 2

@description('VM SKU for the default pool. B2s is the cheapest that runs the operator + KPS.')
param nodeVmSize string = 'Standard_B2s'

@description('Tags to apply to every resource for cost tracking.')
param tags object = {
  project: 'gpu-k8s-operator'
  environment: 'dev'
}

var clusterName = '${environmentPrefix}-aks'
var registryName = toLower('${environmentPrefix}registry${uniqueString(resourceGroup().id)}')

module acr 'modules/acr.bicep' = {
  name: 'acr'
  params: {
    name: registryName
    location: location
    tags: tags
  }
}

module aks 'modules/aks.bicep' = {
  name: 'aks'
  params: {
    name: clusterName
    location: location
    kubernetesVersion: kubernetesVersion
    nodeCount: nodeCount
    nodeVmSize: nodeVmSize
    registryResourceId: acr.outputs.registryResourceId
    tags: tags
  }
}

output clusterName string = aks.outputs.clusterName
output clusterResourceGroup string = resourceGroup().name
output registryLoginServer string = acr.outputs.loginServer
output registryName string = acr.outputs.registryName
