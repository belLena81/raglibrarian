CREATE SCHEMA IF NOT EXISTS identity;
CREATE SCHEMA IF NOT EXISTS catalog;
\getenv identity_db_password IDENTITY_DB_PASSWORD
\getenv catalog_db_password CATALOG_DB_PASSWORD
CREATE ROLE identity_role LOGIN PASSWORD :'identity_db_password';
CREATE ROLE catalog_role LOGIN PASSWORD :'catalog_db_password';
GRANT USAGE, CREATE ON SCHEMA identity TO identity_role;
GRANT USAGE, CREATE ON SCHEMA catalog TO catalog_role;
