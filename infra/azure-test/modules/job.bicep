param name string
param location string
param environmentId string
param identityId string
param registryServer string
param image string
param queueName string
param maxExecutions int
param testHostName string
param secretFiles array
param runtimeEnvironment object
param rabbitMQSecretName string
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
      triggerType: 'Event'
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
      eventTriggerConfig: {
        parallelism: 1
        replicaCompletionCount: 1
        scale: {
          minExecutions: 0
          maxExecutions: maxExecutions
          pollingInterval: 30
          rules: [
            {
              name: 'rabbitmq-one-message'
              custom: {
                type: 'rabbitmq'
                metadata: {
                  queueName: queueName
                  mode: 'QueueLength'
                  value: '1'
                  protocol: 'amqps'
                }
                auth: [
                  {
                    secretRef: rabbitMQSecretName
                    triggerParameter: 'host'
                  }
                ]
              }
            }
          ]
        }
      }
    }
    template: {
      containers: [
        {
          name: 'adapter'
          image: image
          resources: {
            cpu: 1.0
            memory: '2Gi'
          }
          env: concat([
            { name: 'RAGLIBRARIAN_TEST_HOST', value: testHostName }
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
