// Azure + Teams runtime infrastructure. This template deliberately accepts
// Key Vault *secret names*, never secret values. `kei bot deploy azure` writes
// the Kei runtime credential directly to Key Vault before invoking this file.

@description('Azure region for the Container Apps resources.')
param location string = resourceGroup().location

@description('Kei runtime installation ID. This is diagnostic metadata only; the runtime credential is authoritative.')
param installationId string

@description('Name of the customer-owned Key Vault in this resource group.')
param keyVaultName string

@description('Name of the Key Vault secret containing the Kei runtime credential.')
param runtimeTokenSecretName string

@description('Name of the existing Key Vault secret containing the Microsoft Entra app client secret.')
param teamsAppPasswordSecretName string

@description('Microsoft Entra application (client) ID used by the Teams bot.')
param teamsAppId string

@description('Microsoft Entra tenant ID for the Teams bot.')
param teamsTenantId string

@description('Public control-plane runtime endpoint used by the bot runtime.')
param runtimeControlPlaneUrl string

@description('Published Kei Bot Runtime OCI image.')
param runtimeImage string

@description('Container App name.')
param containerAppName string

@description('Container Apps environment name.')
param containerAppsEnvironmentName string

@description('User-assigned managed identity name.')
param managedIdentityName string

@description('Azure Bot resource name.')
param botResourceName string = containerAppName

@description('Tags applied to every resource created by this installation.')
param tags object = {
  managedBy: 'kei-bot-cli'
  keiInstallationId: installationId
}

var keyVaultSecretsUserRoleDefinitionId = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '4633458b-17de-408a-b874-0445c86b69e6')

resource keyVault 'Microsoft.KeyVault/vaults@2023-07-01' existing = {
  name: keyVaultName
}

resource managedIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: managedIdentityName
  location: location
  tags: tags
}

// The runtime needs data-plane read access to its two Key Vault references.
resource keyVaultSecretsUser 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(keyVault.id, managedIdentity.id, keyVaultSecretsUserRoleDefinitionId)
  scope: keyVault
  properties: {
    roleDefinitionId: keyVaultSecretsUserRoleDefinitionId
    principalId: managedIdentity.properties.principalId
    principalType: 'ServicePrincipal'
  }
}

resource logAnalytics 'Microsoft.OperationalInsights/workspaces@2023-09-01' = {
  name: '${containerAppName}-logs'
  location: location
  tags: tags
  properties: {
    sku: {
      name: 'PerGB2018'
    }
    retentionInDays: 30
  }
}

resource containerAppsEnvironment 'Microsoft.App/managedEnvironments@2024-03-01' = {
  name: containerAppsEnvironmentName
  location: location
  tags: tags
  properties: {
    appLogsConfiguration: {
      destination: 'log-analytics'
      logAnalyticsConfiguration: {
        customerId: logAnalytics.properties.customerId
        sharedKey: logAnalytics.listKeys().primarySharedKey
      }
    }
  }
}

resource runtimeApp 'Microsoft.App/containerApps@2024-03-01' = {
  name: containerAppName
  location: location
  tags: tags
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${managedIdentity.id}': {}
    }
  }
  properties: {
    managedEnvironmentId: containerAppsEnvironment.id
    configuration: {
      activeRevisionsMode: 'Single'
      ingress: {
        external: true
        targetPort: 8080
        transport: 'auto'
      }
      secrets: [
        {
          name: 'kei-runtime-token'
          keyVaultUrl: '${keyVault.properties.vaultUri}secrets/${runtimeTokenSecretName}'
          identity: managedIdentity.id
        }
        {
          name: 'teams-app-password'
          keyVaultUrl: '${keyVault.properties.vaultUri}secrets/${teamsAppPasswordSecretName}'
          identity: managedIdentity.id
        }
      ]
    }
    template: {
      containers: [
        {
          name: 'kei-bot-runtime'
          image: runtimeImage
          env: [
            {
              name: 'KEI_RUNTIME_TOKEN'
              secretRef: 'kei-runtime-token'
            }
            {
              name: 'KEI_RUNTIME_INSTALLATION_ID'
              value: installationId
            }
            {
              name: 'KEI_RUNTIME_CONTROL_PLANE_URL'
              value: runtimeControlPlaneUrl
            }
            {
              name: 'MICROSOFT_APP_ID'
              value: teamsAppId
            }
            {
              name: 'MICROSOFT_APP_TENANT_ID'
              value: teamsTenantId
            }
            {
              name: 'MICROSOFT_APP_TYPE'
              value: 'SingleTenant'
            }
            {
              name: 'MICROSOFT_APP_PASSWORD'
              secretRef: 'teams-app-password'
            }
            {
              name: 'PORT'
              value: '8080'
            }
          ]
          resources: {
            cpu: json('0.5')
            memory: '1Gi'
          }
        }
      ]
      scale: {
        minReplicas: 1
        maxReplicas: 1
      }
    }
  }
  dependsOn: [
    keyVaultSecretsUser
  ]
}

resource azureBot 'Microsoft.BotService/botServices@2023-09-15-preview' = {
  name: botResourceName
  location: 'global'
  kind: 'azurebot'
  sku: {
    name: 'F0'
  }
  tags: tags
  properties: {
    displayName: botResourceName
    endpoint: 'https://${runtimeApp.properties.configuration.ingress.fqdn}/api/messages'
    msaAppId: teamsAppId
    msaAppType: 'SingleTenant'
    msaAppTenantId: teamsTenantId
  }
}

resource teamsChannel 'Microsoft.BotService/botServices/channels@2022-09-15' = {
  parent: azureBot
  name: 'MsTeamsChannel'
  location: 'global'
  kind: 'azurebot'
  sku: {
    name: 'F0'
  }
  properties: {
    channelName: 'MsTeamsChannel'
  }
}

output containerAppFqdn string = runtimeApp.properties.configuration.ingress.fqdn
output botEndpoint string = 'https://${runtimeApp.properties.configuration.ingress.fqdn}/api/messages'
output runtimeTokenSecretReference string = '${keyVault.properties.vaultUri}secrets/${runtimeTokenSecretName}'
