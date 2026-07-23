targetScope = 'resourceGroup'

@description('Short, DNS-safe name for isolated test resources.')
param environmentName string

@description('Region for Container Apps and supporting resources.')
param location string = resourceGroup().location

@description('Existing delegated subnet used by the private Container Apps environment.')
param infrastructureSubnetId string

@description('Existing private DNS-enabled Key Vault. This template never creates secret values.')
param keyVaultName string

@description('Existing private Azure Container Registry containing reviewed immutable images.')
param containerRegistryName string

@description('Existing VM private address or resolvable private DNS name. Jobs use it only through the VNet.')
param testHostName string

@description('Immutable image references for each one-shot adapter. Every value must include an @sha256 digest.')
param images object

@description('Version IDs for the fixed Key Vault secret names. Secret names and vault are not caller-configurable.')
param secretVersions object

@description('Non-secret service environment settings, keyed by ingestion, planner, and index.')
param runtimeEnvironments object

@description('RabbitMQ queues consumed by the provider-neutral adapters.')
param queues object = {
  ingestion: 'ingestion.book-uploaded.v1'
  plannerBookUploaded: 'retrieval.book-uploaded.v1'
  plannerChunksReady: 'retrieval.chunks-ready.v1'
  plannerLifecycle: 'retrieval.book-lifecycle.v1'
  index: 'retrieval.index-batch.v1'
}

@description('Deploy jobs disabled until the protected workflow explicitly activates serverless mode.')
@allowed([
  'paused'
  'serverless'
])
param processingMode string = 'paused'

var commonTags = {
  application: 'raglibrarian'
  environment: environmentName
  purpose: 'protected-test'
  dataClassification: 'authorized-synthetic-test-only'
}
var enabled = processingMode == 'serverless'
var secretUris = {
  ingestionPostgresDSN: 'https://${keyVaultName}.vault.azure.net/secrets/ingestion-postgres-dsn/${secretVersions.ingestionPostgresDSN}'
  ingestionRabbitMQ: 'https://${keyVaultName}.vault.azure.net/secrets/ingestion-rabbitmq-uri/${secretVersions.ingestionRabbitMQ}'
  ingestionMinioAccessKey: 'https://${keyVaultName}.vault.azure.net/secrets/ingestion-minio-access-key/${secretVersions.ingestionMinioAccessKey}'
  ingestionMinioSecretKey: 'https://${keyVaultName}.vault.azure.net/secrets/ingestion-minio-secret-key/${secretVersions.ingestionMinioSecretKey}'
  plannerPostgresDSN: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-planner-postgres-dsn/${secretVersions.plannerPostgresDSN}'
  plannerRabbitMQConsumer: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-planner-rabbitmq-consumer-uri/${secretVersions.plannerRabbitMQConsumer}'
  plannerRabbitMQPublisher: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-planner-rabbitmq-publisher-uri/${secretVersions.plannerRabbitMQPublisher}'
  plannerMinioAccessKey: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-planner-minio-access-key/${secretVersions.plannerMinioAccessKey}'
  plannerMinioSecretKey: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-planner-minio-secret-key/${secretVersions.plannerMinioSecretKey}'
  plannerQdrantAPIKey: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-planner-qdrant-api-key/${secretVersions.plannerQdrantAPIKey}'
  indexPostgresDSN: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-index-postgres-dsn/${secretVersions.indexPostgresDSN}'
  indexRabbitMQConsumer: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-index-rabbitmq-consumer-uri/${secretVersions.indexRabbitMQConsumer}'
  indexRabbitMQPublisher: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-index-rabbitmq-publisher-uri/${secretVersions.indexRabbitMQPublisher}'
  indexMinioAccessKey: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-index-minio-access-key/${secretVersions.indexMinioAccessKey}'
  indexMinioSecretKey: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-index-minio-secret-key/${secretVersions.indexMinioSecretKey}'
  indexQdrantAPIKey: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-index-qdrant-api-key/${secretVersions.indexQdrantAPIKey}'
  ingestionDispatcherPostgresDSN: 'https://${keyVaultName}.vault.azure.net/secrets/ingestion-dispatcher-postgres-dsn/${secretVersions.ingestionDispatcherPostgresDSN}'
  ingestionDispatcherRabbitMQ: 'https://${keyVaultName}.vault.azure.net/secrets/ingestion-dispatcher-rabbitmq-uri/${secretVersions.ingestionDispatcherRabbitMQ}'
  ingestionCleanupPostgresDSN: 'https://${keyVaultName}.vault.azure.net/secrets/ingestion-cleanup-postgres-dsn/${secretVersions.ingestionCleanupPostgresDSN}'
  ingestionCleanupMinioAccessKey: 'https://${keyVaultName}.vault.azure.net/secrets/ingestion-cleanup-minio-access-key/${secretVersions.ingestionCleanupMinioAccessKey}'
  ingestionCleanupMinioSecretKey: 'https://${keyVaultName}.vault.azure.net/secrets/ingestion-cleanup-minio-secret-key/${secretVersions.ingestionCleanupMinioSecretKey}'
  retrievalDispatcherPostgresDSN: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-dispatcher-postgres-dsn/${secretVersions.retrievalDispatcherPostgresDSN}'
  retrievalDispatcherRabbitMQPublisher: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-dispatcher-rabbitmq-publisher-uri/${secretVersions.retrievalDispatcherRabbitMQPublisher}'
  retrievalCleanupPostgresDSN: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-cleanup-postgres-dsn/${secretVersions.retrievalCleanupPostgresDSN}'
  retrievalCleanupQdrantAPIKey: 'https://${keyVaultName}.vault.azure.net/secrets/retrieval-cleanup-qdrant-api-key/${secretVersions.retrievalCleanupQdrantAPIKey}'
}
var ingestionSecretFiles = [
  { name: 'postgres-dsn', path: 'ingestion_postgres_dsn', environmentName: 'INGESTION_POSTGRES_DSN_FILE', uri: secretUris.ingestionPostgresDSN }
  { name: 'rabbitmq-uri', path: 'ingestion_rabbitmq_uri', environmentName: 'INGESTION_RABBITMQ_URI_FILE', uri: secretUris.ingestionRabbitMQ }
  { name: 'minio-access-key', path: 'ingestion_minio_access_key', environmentName: 'INGESTION_MINIO_ACCESS_KEY_FILE', uri: secretUris.ingestionMinioAccessKey }
  { name: 'minio-secret-key', path: 'ingestion_minio_secret_key', environmentName: 'INGESTION_MINIO_SECRET_KEY_FILE', uri: secretUris.ingestionMinioSecretKey }
]
var plannerSecretFiles = [
  { name: 'postgres-dsn', path: 'retrieval_planner_postgres_dsn', environmentName: 'RETRIEVAL_POSTGRES_DSN_FILE', uri: secretUris.plannerPostgresDSN }
  { name: 'rabbitmq-consumer-uri', path: 'retrieval_planner_consumer_rabbitmq_uri', environmentName: 'RETRIEVAL_RABBITMQ_CONSUMER_URI_FILE', uri: secretUris.plannerRabbitMQConsumer }
  { name: 'rabbitmq-publisher-uri', path: 'retrieval_planner_publisher_rabbitmq_uri', environmentName: 'RETRIEVAL_RABBITMQ_PUBLISHER_URI_FILE', uri: secretUris.plannerRabbitMQPublisher }
  { name: 'minio-access-key', path: 'retrieval_planner_minio_access_key', environmentName: 'RETRIEVAL_MINIO_ACCESS_KEY_FILE', uri: secretUris.plannerMinioAccessKey }
  { name: 'minio-secret-key', path: 'retrieval_planner_minio_secret_key', environmentName: 'RETRIEVAL_MINIO_SECRET_KEY_FILE', uri: secretUris.plannerMinioSecretKey }
  { name: 'qdrant-api-key', path: 'retrieval_planner_qdrant_api_key', environmentName: 'RETRIEVAL_QDRANT_API_KEY_FILE', uri: secretUris.plannerQdrantAPIKey }
]
var indexSecretFiles = [
  { name: 'postgres-dsn', path: 'retrieval_index_postgres_dsn', environmentName: 'RETRIEVAL_POSTGRES_DSN_FILE', uri: secretUris.indexPostgresDSN }
  { name: 'rabbitmq-consumer-uri', path: 'retrieval_index_consumer_rabbitmq_uri', environmentName: 'RETRIEVAL_RABBITMQ_CONSUMER_URI_FILE', uri: secretUris.indexRabbitMQConsumer }
  { name: 'rabbitmq-publisher-uri', path: 'retrieval_index_publisher_rabbitmq_uri', environmentName: 'RETRIEVAL_RABBITMQ_PUBLISHER_URI_FILE', uri: secretUris.indexRabbitMQPublisher }
  { name: 'minio-access-key', path: 'retrieval_index_minio_access_key', environmentName: 'RETRIEVAL_MINIO_ACCESS_KEY_FILE', uri: secretUris.indexMinioAccessKey }
  { name: 'minio-secret-key', path: 'retrieval_index_minio_secret_key', environmentName: 'RETRIEVAL_MINIO_SECRET_KEY_FILE', uri: secretUris.indexMinioSecretKey }
  { name: 'qdrant-api-key', path: 'retrieval_index_qdrant_api_key', environmentName: 'RETRIEVAL_QDRANT_API_KEY_FILE', uri: secretUris.indexQdrantAPIKey }
]
var ingestionDispatcherSecretFiles = [
  { name: 'postgres-dsn', path: 'ingestion_dispatcher_postgres_dsn', environmentName: 'INGESTION_POSTGRES_DSN_FILE', uri: secretUris.ingestionDispatcherPostgresDSN }
  { name: 'rabbitmq-uri', path: 'ingestion_dispatcher_rabbitmq_uri', environmentName: 'INGESTION_RABBITMQ_URI_FILE', uri: secretUris.ingestionDispatcherRabbitMQ }
]
var ingestionCleanupSecretFiles = [
  { name: 'postgres-dsn', path: 'ingestion_cleanup_postgres_dsn', environmentName: 'INGESTION_CLEANUP_POSTGRES_DSN_FILE', uri: secretUris.ingestionCleanupPostgresDSN }
  { name: 'minio-access-key', path: 'ingestion_cleanup_minio_access_key', environmentName: 'INGESTION_CLEANUP_MINIO_ACCESS_KEY_FILE', uri: secretUris.ingestionCleanupMinioAccessKey }
  { name: 'minio-secret-key', path: 'ingestion_cleanup_minio_secret_key', environmentName: 'INGESTION_CLEANUP_MINIO_SECRET_KEY_FILE', uri: secretUris.ingestionCleanupMinioSecretKey }
]
var retrievalDispatcherSecretFiles = [
  { name: 'postgres-dsn', path: 'retrieval_dispatcher_postgres_dsn', environmentName: 'RETRIEVAL_POSTGRES_DSN_FILE', uri: secretUris.retrievalDispatcherPostgresDSN }
  { name: 'rabbitmq-publisher-uri', path: 'retrieval_dispatcher_publisher_rabbitmq_uri', environmentName: 'RETRIEVAL_RABBITMQ_PUBLISHER_URI_FILE', uri: secretUris.retrievalDispatcherRabbitMQPublisher }
]
var retrievalCleanupSecretFiles = [
  { name: 'postgres-dsn', path: 'retrieval_cleanup_postgres_dsn', environmentName: 'RETRIEVAL_POSTGRES_DSN_FILE', uri: secretUris.retrievalCleanupPostgresDSN }
  { name: 'qdrant-api-key', path: 'retrieval_cleanup_qdrant_api_key', environmentName: 'RETRIEVAL_QDRANT_API_KEY_FILE', uri: secretUris.retrievalCleanupQdrantAPIKey }
]
// Role scopes come from the same fixed URI mappings used by Container Apps.
// Callers cannot widen an identity by supplying an independent secret-name list.
var ingestionSecretNames = [for secretFile in ingestionSecretFiles: split(secretFile.uri, '/')[4]]
var plannerSecretNames = [for secretFile in plannerSecretFiles: split(secretFile.uri, '/')[4]]
var indexSecretNames = [for secretFile in indexSecretFiles: split(secretFile.uri, '/')[4]]
var ingestionDispatcherSecretNames = [for secretFile in ingestionDispatcherSecretFiles: split(secretFile.uri, '/')[4]]
var ingestionCleanupSecretNames = [for secretFile in ingestionCleanupSecretFiles: split(secretFile.uri, '/')[4]]
var retrievalDispatcherSecretNames = [for secretFile in retrievalDispatcherSecretFiles: split(secretFile.uri, '/')[4]]
var retrievalCleanupSecretNames = [for secretFile in retrievalCleanupSecretFiles: split(secretFile.uri, '/')[4]]

resource keyVault 'Microsoft.KeyVault/vaults@2023-07-01' existing = {
  name: keyVaultName
}

resource registry 'Microsoft.ContainerRegistry/registries@2023-07-01' existing = {
  name: containerRegistryName
}

module ingestionIdentity 'modules/job-identity.bicep' = {
  name: '${environmentName}-ingestion-identity'
  params: { name: '${environmentName}-ingestion-job', location: location, registryName: containerRegistryName, keyVaultName: keyVaultName, secretNames: ingestionSecretNames, tags: commonTags }
}
module plannerUploadedIdentity 'modules/job-identity.bicep' = {
  name: '${environmentName}-planner-uploaded-identity'
  params: { name: '${environmentName}-planner-uploaded-job', location: location, registryName: containerRegistryName, keyVaultName: keyVaultName, secretNames: plannerSecretNames, tags: commonTags }
}
module plannerChunksIdentity 'modules/job-identity.bicep' = {
  name: '${environmentName}-planner-chunks-identity'
  params: { name: '${environmentName}-planner-chunks-job', location: location, registryName: containerRegistryName, keyVaultName: keyVaultName, secretNames: plannerSecretNames, tags: commonTags }
}
module plannerLifecycleIdentity 'modules/job-identity.bicep' = {
  name: '${environmentName}-planner-lifecycle-identity'
  params: { name: '${environmentName}-planner-lifecycle-job', location: location, registryName: containerRegistryName, keyVaultName: keyVaultName, secretNames: plannerSecretNames, tags: commonTags }
}
module indexIdentity 'modules/job-identity.bicep' = {
  name: '${environmentName}-index-identity'
  params: { name: '${environmentName}-index-job', location: location, registryName: containerRegistryName, keyVaultName: keyVaultName, secretNames: indexSecretNames, tags: commonTags }
}
module ingestionDispatcherIdentity 'modules/job-identity.bicep' = {
  name: '${environmentName}-ingestion-dispatcher-identity'
  params: { name: '${environmentName}-ingestion-dispatcher-job', location: location, registryName: containerRegistryName, keyVaultName: keyVaultName, secretNames: ingestionDispatcherSecretNames, tags: commonTags }
}
module ingestionCleanupIdentity 'modules/job-identity.bicep' = {
  name: '${environmentName}-ingestion-cleanup-identity'
  params: { name: '${environmentName}-ingestion-cleanup-job', location: location, registryName: containerRegistryName, keyVaultName: keyVaultName, secretNames: ingestionCleanupSecretNames, tags: commonTags }
}
module retrievalDispatcherIdentity 'modules/job-identity.bicep' = {
  name: '${environmentName}-retrieval-dispatcher-identity'
  params: { name: '${environmentName}-retrieval-dispatcher-job', location: location, registryName: containerRegistryName, keyVaultName: keyVaultName, secretNames: retrievalDispatcherSecretNames, tags: commonTags }
}
module retrievalCleanupIdentity 'modules/job-identity.bicep' = {
  name: '${environmentName}-retrieval-cleanup-identity'
  params: { name: '${environmentName}-retrieval-cleanup-job', location: location, registryName: containerRegistryName, keyVaultName: keyVaultName, secretNames: retrievalCleanupSecretNames, tags: commonTags }
}

resource containerAppsEnvironment 'Microsoft.App/managedEnvironments@2024-03-01' = {
  name: '${environmentName}-cae'
  location: location
  tags: commonTags
  properties: {
    vnetConfiguration: {
      infrastructureSubnetId: infrastructureSubnetId
      internal: true
    }
    workloadProfiles: [
      {
        name: 'Consumption'
        workloadProfileType: 'Consumption'
      }
    ]
  }
}

// Each job receives only its dedicated Key Vault references. The identity is
// granted Key Vault Secrets User and AcrPull outside this template, after the
// caller has reviewed the exact vault, registry, and secret scope.
module ingestionJob 'modules/job.bicep' = if (enabled) {
  name: '${environmentName}-ingestion-job'
  params: {
    name: '${environmentName}-ingestion'
    location: location
    environmentId: containerAppsEnvironment.id
    identityId: ingestionIdentity.outputs.id
    registryServer: registry.properties.loginServer
    image: images.ingestion
    queueName: queues.ingestion
    maxExecutions: 2
    testHostName: testHostName
    secretFiles: ingestionSecretFiles
    runtimeEnvironment: runtimeEnvironments.ingestion
    rabbitMQSecretName: 'rabbitmq-uri'
    tags: commonTags
  }
}

module plannerBookUploadedJob 'modules/job.bicep' = if (enabled) {
  name: '${environmentName}-planner-uploaded-job'
  params: {
    name: '${environmentName}-planner-uploaded'
    location: location
    environmentId: containerAppsEnvironment.id
    identityId: plannerUploadedIdentity.outputs.id
    registryServer: registry.properties.loginServer
    image: images.planner
    queueName: queues.plannerBookUploaded
    maxExecutions: 4
    testHostName: testHostName
    secretFiles: plannerSecretFiles
    runtimeEnvironment: union(runtimeEnvironments.planner, { RETRIEVAL_SERVERLESS_QUEUE: queues.plannerBookUploaded })
    rabbitMQSecretName: 'rabbitmq-consumer-uri'
    tags: commonTags
  }
}

module plannerChunksReadyJob 'modules/job.bicep' = if (enabled) {
  name: '${environmentName}-planner-chunks-job'
  params: {
    name: '${environmentName}-planner-chunks'
    location: location
    environmentId: containerAppsEnvironment.id
    identityId: plannerChunksIdentity.outputs.id
    registryServer: registry.properties.loginServer
    image: images.planner
    queueName: queues.plannerChunksReady
    maxExecutions: 4
    testHostName: testHostName
    secretFiles: plannerSecretFiles
    runtimeEnvironment: union(runtimeEnvironments.planner, { RETRIEVAL_SERVERLESS_QUEUE: queues.plannerChunksReady })
    rabbitMQSecretName: 'rabbitmq-consumer-uri'
    tags: commonTags
  }
}

module plannerLifecycleJob 'modules/job.bicep' = if (enabled) {
  name: '${environmentName}-planner-lifecycle-job'
  params: {
    name: '${environmentName}-planner-lifecycle'
    location: location
    environmentId: containerAppsEnvironment.id
    identityId: plannerLifecycleIdentity.outputs.id
    registryServer: registry.properties.loginServer
    image: images.planner
    queueName: queues.plannerLifecycle
    maxExecutions: 4
    testHostName: testHostName
    secretFiles: plannerSecretFiles
    runtimeEnvironment: union(runtimeEnvironments.planner, { RETRIEVAL_SERVERLESS_QUEUE: queues.plannerLifecycle })
    rabbitMQSecretName: 'rabbitmq-consumer-uri'
    tags: commonTags
  }
}

module indexJob 'modules/job.bicep' = if (enabled) {
  name: '${environmentName}-index-job'
  params: {
    name: '${environmentName}-index'
    location: location
    environmentId: containerAppsEnvironment.id
    identityId: indexIdentity.outputs.id
    registryServer: registry.properties.loginServer
    image: images.index
    queueName: queues.index
    maxExecutions: 4
    testHostName: testHostName
    secretFiles: indexSecretFiles
    runtimeEnvironment: union(runtimeEnvironments.index, { RETRIEVAL_SERVERLESS_QUEUE: queues.index })
    rabbitMQSecretName: 'rabbitmq-consumer-uri'
    tags: commonTags
  }
}

// Scheduled jobs do not exist while paused. Deleting them prevents a cron
// trigger from racing the worker/serverless handoff.
module ingestionDispatcherJob 'modules/scheduled-job.bicep' = if (enabled) {
  name: '${environmentName}-ingestion-dispatcher-job'
  params: {
    name: '${environmentName}-ingestion-dispatcher'
    location: location
    environmentId: containerAppsEnvironment.id
    identityId: ingestionDispatcherIdentity.outputs.id
    registryServer: registry.properties.loginServer
    image: images.ingestionDispatcher
    cronExpression: '* * * * *'
    secretFiles: ingestionDispatcherSecretFiles
    runtimeEnvironment: runtimeEnvironments.ingestionDispatcher
    tags: commonTags
  }
}

module ingestionCleanupJob 'modules/scheduled-job.bicep' = if (enabled) {
  name: '${environmentName}-ingestion-cleanup-job'
  params: {
    name: '${environmentName}-ingestion-cleanup'
    location: location
    environmentId: containerAppsEnvironment.id
    identityId: ingestionCleanupIdentity.outputs.id
    registryServer: registry.properties.loginServer
    image: images.ingestionCleanup
    cronExpression: '* * * * *'
    secretFiles: ingestionCleanupSecretFiles
    runtimeEnvironment: runtimeEnvironments.ingestionCleanup
    tags: commonTags
  }
}

module retrievalDispatcherJob 'modules/scheduled-job.bicep' = if (enabled) {
  name: '${environmentName}-retrieval-dispatcher-job'
  params: {
    name: '${environmentName}-retrieval-dispatcher'
    location: location
    environmentId: containerAppsEnvironment.id
    identityId: retrievalDispatcherIdentity.outputs.id
    registryServer: registry.properties.loginServer
    image: images.retrievalDispatcher
    cronExpression: '* * * * *'
    secretFiles: retrievalDispatcherSecretFiles
    runtimeEnvironment: runtimeEnvironments.retrievalDispatcher
    tags: commonTags
  }
}

module retrievalCleanupJob 'modules/scheduled-job.bicep' = if (enabled) {
  name: '${environmentName}-retrieval-cleanup-job'
  params: {
    name: '${environmentName}-retrieval-cleanup'
    location: location
    environmentId: containerAppsEnvironment.id
    identityId: retrievalCleanupIdentity.outputs.id
    registryServer: registry.properties.loginServer
    image: images.retrievalCleanup
    cronExpression: '*/15 * * * *'
    secretFiles: retrievalCleanupSecretFiles
    runtimeEnvironment: runtimeEnvironments.retrievalCleanup
    tags: commonTags
  }
}

output managedEnvironmentId string = containerAppsEnvironment.id
output processingMode string = processingMode
