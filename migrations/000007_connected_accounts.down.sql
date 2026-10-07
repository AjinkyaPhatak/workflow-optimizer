DROP TABLE IF EXISTS connected_accounts;
ALTER TABLE credentials DROP CONSTRAINT IF EXISTS credentials_id_workspace_key;
