param name string
param location string
param registryName string
param keyVaultName string
param secretNames array
param tags object

var acrPullRoleDefinitionID = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '7f951dda-4ed3-4680-a7ca-43fe172d538d')
var keyVaultSecretsUserRoleDefinitionID = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '4633458b-17de-408a-b874-0445c86b69e6')

resource identity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: name
  location: location
  tags: tags
}

resource registry 'Microsoft.ContainerRegistry/registries@2023-07-01' existing = {
  name: registryName
}

resource keyVault 'Microsoft.KeyVault/vaults@2023-07-01' existing = {
  name: keyVaultName
}

resource secrets 'Microsoft.KeyVault/vaults/secrets@2023-07-01' existing = [for secretName in secretNames: {
  parent: keyVault
  name: secretName
}]

resource acrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(registry.id, identity.id, acrPullRoleDefinitionID)
  scope: registry
  properties: {
    roleDefinitionId: acrPullRoleDefinitionID
    principalId: identity.properties.principalId
    principalType: 'ServicePrincipal'
  }
}

resource secretReadGrants 'Microsoft.Authorization/roleAssignments@2022-04-01' = [for (secretName, index) in secretNames: {
  name: guid(secrets[index].id, identity.id, keyVaultSecretsUserRoleDefinitionID)
  scope: secrets[index]
  properties: {
    roleDefinitionId: keyVaultSecretsUserRoleDefinitionID
    principalId: identity.properties.principalId
    principalType: 'ServicePrincipal'
  }
}]

output id string = identity.id
