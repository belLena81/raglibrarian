param name string
param location string
param environmentId string
param identityId string
param registryServer string
param image string
param cronExpression string
param secretFiles array
param runtimeEnvironment object
param tags object

resource job 'Microsoft.App/jobs@2024-03-01' = {
  name: name
  location: location
  tags: tags
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${identityId}': {}
    }
  }
  properties: {
    environmentId: environmentId
    configuration: {
      triggerType: 'Schedule'
      replicaRetryLimit: 0
      replicaTimeout: 780
      registries: [
        {
          server: registryServer
          identity: identityId
        }
      ]
      secrets: [for secretFile in secretFiles: {
          name: secretFile.name
          keyVaultUrl: secretFile.uri
          identity: identityId
        }]
      scheduleTriggerConfig: {
        cronExpression: cronExpression
        parallelism: 1
        replicaCompletionCount: 1
      }
    }
    template: {
      containers: [
        {
          name: 'adapter'
          image: image
          resources: {
            cpu: 0.5
            memory: '1Gi'
          }
          env: concat([
            { name: 'RETRIEVAL_PROCESSING_MODE', value: 'worker' }
          ], [for setting in items(runtimeEnvironment): {
              name: setting.key
              value: string(setting.value)
            }], [for secretFile in secretFiles: {
              name: secretFile.environmentName
              value: '/mnt/secrets/${secretFile.path}'
            }])
          volumeMounts: [
            {
              volumeName: 'runtime-secrets'
              mountPath: '/mnt/secrets'
              readOnly: true
            }
          ]
        }
      ]
      volumes: [
        {
          name: 'runtime-secrets'
          storageType: 'Secret'
          secrets: [for secretFile in secretFiles: {
              secretRef: secretFile.name
              path: secretFile.path
            }]
        }
      ]
    }
  }
}
