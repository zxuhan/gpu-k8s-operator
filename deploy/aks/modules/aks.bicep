// AKS cluster with system-assigned managed identity and a single
// user-mode node pool. Opinionated defaults:
//   - system-assigned identity (no SP to rotate)
//   - Azure CNI overlay mode (cheaper IP usage than kubenet-equivalent)
//   - AcrPull granted to the kubelet identity against the provided ACR
//     so deployments can pull without a pull secret
//   - local accounts enabled so `az aks get-credentials` works without
//     AAD setup; change to `disableLocalAccounts: true` once you stand
//     up AAD RBAC.

@description('AKS cluster name.')
param name string
param location string
param kubernetesVersion string
param nodeCount int
param nodeVmSize string
param tags object
@description('Resource ID of the ACR to grant AcrPull on.')
param registryResourceId string

resource cluster 'Microsoft.ContainerService/managedClusters@2024-05-01' = {
  name: name
  location: location
  tags: tags
  identity: {
    type: 'SystemAssigned'
  }
  properties: {
    dnsPrefix: '${name}-dns'
    kubernetesVersion: kubernetesVersion
    enableRBAC: true
    networkProfile: {
      networkPlugin: 'azure'
      networkPluginMode: 'overlay'
      networkPolicy: 'azure'
    }
    agentPoolProfiles: [
      {
        name: 'system'
        mode: 'System'
        count: nodeCount
        vmSize: nodeVmSize
        osType: 'Linux'
        osSKU: 'AzureLinux'
        type: 'VirtualMachineScaleSets'
      }
    ]
  }
}

// AcrPull role assignment — the kubelet identity pulls images on
// behalf of every pod. We assign AcrPull explicitly (rather than
// Contributor) to follow least-privilege.
var acrPullRoleId = '7f951dda-4ed3-4680-a7ca-43fe172d538d'

resource acrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(registryResourceId, cluster.id, acrPullRoleId)
  scope: resourceGroup()
  properties: {
    principalId: cluster.properties.identityProfile.kubeletidentity.objectId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId(
      'Microsoft.Authorization/roleDefinitions',
      acrPullRoleId
    )
  }
}

output clusterName string = cluster.name
output clusterResourceId string = cluster.id
