-- Phase C2: connected accounts (OAuth).
--
-- A connected account is metadata about an external account a workspace
-- authorized. It holds no token: the OAuth material (access token, refresh
-- token, expiry, scopes) is encrypted inside the referenced credential's
-- existing encrypted_data envelope (AES-256-GCM, as for API keys).

-- Lets a connected account reference "a credential of this workspace": the
-- composite foreign key below makes a cross-workspace reference impossible.
ALTER TABLE credentials ADD CONSTRAINT credentials_id_workspace_key UNIQUE (id, workspace_id);

CREATE TABLE connected_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id),
    -- Who connected (or last reconnected) the account.
    user_id UUID NOT NULL REFERENCES users(id),
    provider VARCHAR(64) NOT NULL CHECK (provider <> ''),
    -- The stable external identity (never assumed to be an email).
    provider_account_id VARCHAR(255) NOT NULL CHECK (provider_account_id <> ''),
    display_name VARCHAR(255) NOT NULL DEFAULT '',
    email VARCHAR(320) NOT NULL DEFAULT '',
    credential_id UUID NOT NULL,
    status VARCHAR(16) NOT NULL CHECK (status IN ('ACTIVE', 'EXPIRED', 'REVOKED', 'ERROR', 'DISCONNECTED')),
    scopes TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    -- One account per external identity per workspace: reconnecting updates it.
    CONSTRAINT connected_accounts_identity_key UNIQUE (workspace_id, provider, provider_account_id),
    CONSTRAINT connected_accounts_credential_key UNIQUE (credential_id),
    -- The credential must belong to the same workspace. Deleting a
    -- credential that backs an account is refused.
    CONSTRAINT connected_accounts_credential_fkey FOREIGN KEY (credential_id, workspace_id)
        REFERENCES credentials(id, workspace_id) ON DELETE RESTRICT
);

CREATE INDEX connected_accounts_workspace_idx ON connected_accounts(workspace_id, provider);
