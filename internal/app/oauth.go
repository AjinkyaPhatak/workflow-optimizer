package app

import (
	"log/slog"

	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/connectedaccount"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/infrastructure/postgres"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/integration/calendar"
	"workflow-optimizer/internal/integration/gmail"
	"workflow-optimizer/internal/integration/notion"
	"workflow-optimizer/internal/integration/sheets"
	"workflow-optimizer/internal/oauth"
	"workflow-optimizer/internal/oauth/google"
	oauthnotion "workflow-optimizer/internal/oauth/notion"
)

// Extensions are optional collaborators of the API and worker runtimes
// (Phase C2). The production composition passes none: no OAuth provider and
// no integration is configured yet. Tests install fakes here.
type Extensions struct {
	// OAuthProviders are the OAuth providers accounts can be connected with.
	OAuthProviders []oauth.Provider
	// Integrations builds integration modules around the runtime credential
	// resolver (the OAuth token manager).
	Integrations func(credentials credential.Resolver) []integration.Module
	// Gmail, Sheets and Calendar override the Google API client options
	// (tests).
	Gmail    gmail.Options
	Sheets   sheets.Options
	Calendar calendar.Options
	Notion   notion.Options
}

// oauthProviders are the providers of a runtime: Google (Phase C3) and
// Notion (C5) when their OAuth clients are configured, plus any extension
// providers.
func oauthProviders(cfg config.Config, ext Extensions) ([]oauth.Provider, error) {
	providers := append([]oauth.Provider{}, ext.OAuthProviders...)
	if cfg.GoogleOAuthConfigured() {
		g, err := google.New(google.Options{ClientID: cfg.GoogleClientID, ClientSecret: credential.NewSecret(cfg.GoogleClientSecret),
			RedirectURL: cfg.GoogleOAuthRedirectURI})
		if err != nil {
			return nil, err
		}
		providers = append(providers, g)
	}
	if cfg.NotionOAuthConfigured() {
		n, err := oauthnotion.New(oauthnotion.Options{ClientID: cfg.NotionClientID, ClientSecret: credential.NewSecret(cfg.NotionClientSecret),
			RedirectURL: cfg.NotionOAuthRedirectURI})
		if err != nil {
			return nil, err
		}
		providers = append(providers, n)
	}
	return providers, nil
}

// credentialChain is the runtime credential composition shared by the API
// and the worker:
//
//	AES-256-GCM -> CredentialRepository -> credential.Service
//	-> oauth.TokenManager (refresh, account status) = the credential.Resolver of every node
type credentialChain struct {
	service   *credential.Service
	repo      *postgres.CredentialRepository
	providers *oauth.Registry
	accounts  *connectedaccount.Service
	resolver  *oauth.TokenManager
}

func newCredentialChain(cfg config.Config, store *postgres.Store, enc credential.SecretEncryptor, ext Extensions, logger *slog.Logger) (*credentialChain, error) {
	repo := postgres.NewCredentialRepository(store)
	service, err := credential.NewService(repo, enc)
	if err != nil {
		return nil, err
	}
	list, err := oauthProviders(cfg, ext)
	if err != nil {
		return nil, err
	}
	providers, err := oauth.NewRegistry(list...)
	if err != nil {
		return nil, err
	}
	accounts := connectedaccount.NewService(postgres.NewConnectedAccountRepository(store), service, providers, logger)
	return &credentialChain{service: service, repo: repo, providers: providers, accounts: accounts,
		resolver: oauth.NewTokenManager(service, providers, accounts, logger)}, nil
}

func (c *credentialChain) dependencies(ext Extensions) Dependencies {
	deps := Dependencies{Credentials: c.resolver, Gmail: ext.Gmail, Sheets: ext.Sheets, Calendar: ext.Calendar, Notion: ext.Notion}
	if ext.Integrations != nil {
		deps.Integrations = ext.Integrations(c.resolver)
	}
	return deps
}
