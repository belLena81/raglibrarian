\set ON_ERROR_STOP on

-- These client-side commands read root-owned Compose secrets directly into
-- psql variables. Values never enter Compose environment values, argv, or SQL
-- output and disappear with this one-shot bootstrap process.
\set identity_migration_password `cat /run/secrets/identity_migration_password`
\set identity_runtime_password `cat /run/secrets/identity_runtime_password`

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

\unset identity_migration_password
\unset identity_runtime_password
