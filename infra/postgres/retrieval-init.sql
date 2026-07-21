\set ON_ERROR_STOP on

-- These client-side commands read root-owned Compose secrets directly into
-- psql variables. Values never enter Compose environment values, argv, or SQL
-- output and disappear with this one-shot bootstrap process.
\set retrieval_migration_password `cat /run/secrets/retrieval_migration_password`
\set retrieval_runtime_password `cat /run/secrets/retrieval_runtime_password`
\set retrieval_search_password `cat /run/secrets/retrieval_search_password`
\set retrieval_planner_password `cat /run/secrets/retrieval_planner_password`
\set retrieval_indexer_password `cat /run/secrets/retrieval_indexer_password`
\set retrieval_dispatcher_password `cat /run/secrets/retrieval_dispatcher_password`
\set retrieval_cleanup_password `cat /run/secrets/retrieval_cleanup_password`
\set retrieval_e2e_password `cat /run/secrets/retrieval_e2e_password`

\connect raglibrarian_platform
SELECT format('CREATE ROLE retrieval_migrator LOGIN PASSWORD %L', :'retrieval_migration_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'retrieval_migrator') \gexec
SELECT format('CREATE ROLE retrieval_runtime LOGIN PASSWORD %L', :'retrieval_runtime_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'retrieval_runtime') \gexec
SELECT format('CREATE ROLE retrieval_search LOGIN PASSWORD %L', :'retrieval_search_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'retrieval_search') \gexec
SELECT format('CREATE ROLE retrieval_planner LOGIN PASSWORD %L', :'retrieval_planner_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'retrieval_planner') \gexec
SELECT format('CREATE ROLE retrieval_indexer LOGIN PASSWORD %L', :'retrieval_indexer_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'retrieval_indexer') \gexec
SELECT format('CREATE ROLE retrieval_dispatcher LOGIN PASSWORD %L', :'retrieval_dispatcher_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'retrieval_dispatcher') \gexec
SELECT format('CREATE ROLE retrieval_cleanup LOGIN PASSWORD %L', :'retrieval_cleanup_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'retrieval_cleanup') \gexec
SELECT format('CREATE ROLE retrieval_e2e LOGIN PASSWORD %L', :'retrieval_e2e_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'retrieval_e2e') \gexec
SELECT 'CREATE DATABASE retrieval OWNER retrieval_migrator TEMPLATE template0'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'retrieval') \gexec
REVOKE ALL ON DATABASE retrieval FROM PUBLIC;
GRANT CONNECT ON DATABASE retrieval TO retrieval_runtime;
GRANT CONNECT ON DATABASE retrieval TO retrieval_search;
GRANT CONNECT ON DATABASE retrieval TO retrieval_planner;
GRANT CONNECT ON DATABASE retrieval TO retrieval_indexer;
GRANT CONNECT ON DATABASE retrieval TO retrieval_dispatcher;
GRANT CONNECT ON DATABASE retrieval TO retrieval_cleanup;
GRANT CONNECT ON DATABASE retrieval TO retrieval_e2e;
\connect retrieval
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA IF NOT EXISTS retrieval AUTHORIZATION retrieval_migrator;
REVOKE ALL ON SCHEMA retrieval FROM PUBLIC;
GRANT USAGE ON SCHEMA retrieval TO retrieval_runtime;
GRANT USAGE ON SCHEMA retrieval TO retrieval_search;
GRANT USAGE ON SCHEMA retrieval TO retrieval_planner;
GRANT USAGE ON SCHEMA retrieval TO retrieval_indexer;
GRANT USAGE ON SCHEMA retrieval TO retrieval_dispatcher;
GRANT USAGE ON SCHEMA retrieval TO retrieval_cleanup;
GRANT USAGE ON SCHEMA retrieval TO retrieval_e2e;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA retrieval TO retrieval_runtime;
GRANT SELECT ON ALL TABLES IN SCHEMA retrieval TO retrieval_e2e;
ALTER DEFAULT PRIVILEGES FOR ROLE retrieval_migrator IN SCHEMA retrieval
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO retrieval_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE retrieval_migrator IN SCHEMA retrieval
    GRANT SELECT ON TABLES TO retrieval_e2e;

\unset retrieval_migration_password
\unset retrieval_runtime_password
\unset retrieval_search_password
\unset retrieval_planner_password
\unset retrieval_indexer_password
\unset retrieval_dispatcher_password
\unset retrieval_cleanup_password
\unset retrieval_e2e_password
