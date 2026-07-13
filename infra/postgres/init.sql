CREATE SCHEMA IF NOT EXISTS identity;
CREATE SCHEMA IF NOT EXISTS catalog;
CREATE ROLE identity_role LOGIN PASSWORD 'identity_local';
CREATE ROLE catalog_role LOGIN PASSWORD 'catalog_local';
GRANT USAGE, CREATE ON SCHEMA identity TO identity_role;
GRANT USAGE, CREATE ON SCHEMA catalog TO catalog_role;
