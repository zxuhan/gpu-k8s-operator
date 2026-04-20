// Azure Container Registry — Basic SKU is enough for a handful of
// images and is the cheapest option on a student subscription
// (~$5/month). Admin user is disabled so the only auth path is
// AcrPull role assignments granted to the AKS kubelet identity in
// modules/aks.bicep.

@description('Globally unique registry name (lowercase, 5-50 chars, alphanumeric).')
param name string
param location string
param tags object

resource registry 'Microsoft.ContainerRegistry/registries@2023-07-01' = {
  name: name
  location: location
  tags: tags
  sku: {
    name: 'Basic'
  }
  properties: {
    adminUserEnabled: false
  }
}

output registryName string = registry.name
output loginServer string = registry.properties.loginServer
output registryResourceId string = registry.id
