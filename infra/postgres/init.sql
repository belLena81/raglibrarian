\set ON_ERROR_STOP on

-- These client-side commands read root-owned Compose secrets directly into
-- psql variables. Values never enter Compose environment values, argv, or SQL
-- output and disappear with this one-shot bootstrap process.
\set identity_migration_password `cat /run/secrets/identity_migration_password`
\set identity_runtime_password `cat /run/secrets/identity_runtime_password`
\set catalog_migration_password `cat /run/secrets/catalog_migration_password`
\set catalog_runtime_password `cat /run/secrets/catalog_runtime_password`
\set ingestion_migration_password `cat /run/secrets/ingestion_migration_password`
\set ingestion_runtime_password `cat /run/secrets/ingestion_runtime_password`
\set ingestion_cleanup_password `cat /run/secrets/ingestion_cleanup_password`
\set ingestion_e2e_password `cat /run/secrets/ingestion_e2e_password`

SELECT format('CREATE ROLE identity_migrator LOGIN PASSWORD %L', :'identity_migration_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'identity_migrator') \gexec
SELECT format('CREATE ROLE identity_runtime LOGIN PASSWORD %L', :'identity_runtime_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'identity_runtime') \gexec

SELECT 'CREATE DATABASE identity OWNER identity_migrator TEMPLATE template0'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'identity') \gexec

REVOKE ALL ON DATABASE identity FROM PUBLIC;
GRANT CONNECT ON DATABASE identity TO identity_runtime;

\connect identity

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA IF NOT EXISTS identity AUTHORIZATION identity_migrator;
REVOKE ALL ON SCHEMA identity FROM PUBLIC;
GRANT USAGE ON SCHEMA identity TO identity_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA identity TO identity_runtime;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA identity TO identity_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE identity_migrator IN SCHEMA identity
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO identity_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE identity_migrator IN SCHEMA identity
    GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO identity_runtime;

\connect raglibrarian_platform
SELECT format('CREATE ROLE catalog_migrator LOGIN PASSWORD %L', :'catalog_migration_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'catalog_migrator') \gexec
SELECT format('CREATE ROLE catalog_runtime LOGIN PASSWORD %L', :'catalog_runtime_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'catalog_runtime') \gexec
SELECT 'CREATE DATABASE catalog OWNER catalog_migrator TEMPLATE template0'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'catalog') \gexec
REVOKE ALL ON DATABASE catalog FROM PUBLIC;
GRANT CONNECT ON DATABASE catalog TO catalog_runtime;
\connect catalog
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA IF NOT EXISTS catalog AUTHORIZATION catalog_migrator;
REVOKE ALL ON SCHEMA catalog FROM PUBLIC;
GRANT USAGE ON SCHEMA catalog TO catalog_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA catalog TO catalog_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE catalog_migrator IN SCHEMA catalog
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO catalog_runtime;

\connect raglibrarian_platform
SELECT format('CREATE ROLE ingestion_migrator LOGIN PASSWORD %L', :'ingestion_migration_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ingestion_migrator') \gexec
SELECT format('CREATE ROLE ingestion_runtime LOGIN PASSWORD %L', :'ingestion_runtime_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ingestion_runtime') \gexec
SELECT format('CREATE ROLE ingestion_cleanup LOGIN PASSWORD %L', :'ingestion_cleanup_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ingestion_cleanup') \gexec
SELECT format('CREATE ROLE ingestion_e2e LOGIN PASSWORD %L', :'ingestion_e2e_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ingestion_e2e') \gexec
SELECT 'CREATE DATABASE ingestion OWNER ingestion_migrator TEMPLATE template0'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'ingestion') \gexec
REVOKE ALL ON DATABASE ingestion FROM PUBLIC;
GRANT CONNECT ON DATABASE ingestion TO ingestion_runtime;
GRANT CONNECT ON DATABASE ingestion TO ingestion_cleanup;
GRANT CONNECT ON DATABASE ingestion TO ingestion_e2e;
\connect ingestion
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA IF NOT EXISTS ingestion AUTHORIZATION ingestion_migrator;
REVOKE ALL ON SCHEMA ingestion FROM PUBLIC;
GRANT USAGE ON SCHEMA ingestion TO ingestion_runtime;
GRANT USAGE ON SCHEMA ingestion TO ingestion_cleanup;
GRANT USAGE ON SCHEMA ingestion TO ingestion_e2e;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ingestion TO ingestion_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE ingestion_migrator IN SCHEMA ingestion
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO ingestion_runtime;

\unset identity_migration_password
\unset identity_runtime_password
\unset catalog_migration_password
\unset catalog_runtime_password
\unset ingestion_migration_password
\unset ingestion_runtime_password
\unset ingestion_cleanup_password
\unset ingestion_e2e_password
