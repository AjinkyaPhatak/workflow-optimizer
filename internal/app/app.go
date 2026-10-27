// Package app manages application-level lifecycle, dependency composition,
// and startup bootstrapping for the Workflow Optimizer system.
package app

import (
	"fmt"
	"net/http"

	"workflow-optimizer/internal/compute"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/files"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/integration/airtable"
	"workflow-optimizer/internal/integration/calendar"
	"workflow-optimizer/internal/integration/discord"
	"workflow-optimizer/internal/integration/docs"
	"workflow-optimizer/internal/integration/forms"
	"workflow-optimizer/internal/integration/gmail"
	"workflow-optimizer/internal/integration/meet"
	"workflow-optimizer/internal/integration/notion"
	"workflow-optimizer/internal/integration/sheets"
	"workflow-optimizer/internal/knowledge"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/condition"
	"workflow-optimizer/internal/node/extract"
	nodehttp "workflow-optimizer/internal/node/http"
	"workflow-optimizer/internal/node/input"
	nodejson "workflow-optimizer/internal/node/json"
	"workflow-optimizer/internal/node/kb"
	"workflow-optimizer/internal/node/llm"
	"workflow-optimizer/internal/node/merge"
	"workflow-optimizer/internal/node/output"
	"workflow-optimizer/internal/node/prompt"
	"workflow-optimizer/internal/node/structured_output"
	"workflow-optimizer/internal/node/switchnode"
	"workflow-optimizer/internal/node/text"
	"workflow-optimizer/internal/node/transcribe"
	"workflow-optimizer/internal/node/transform"
	providerllm "workflow-optimizer/internal/provider/llm"
	"workflow-optimizer/internal/provider/llm/openai"
)

// Application encapsulates the runtime registries and configuration of the system.
type Application struct {
	Config           config.Config
	NodeRegistry     node.Registry
	ProviderRegistry providerllm.Registry
	// IntegrationRegistry is discovery metadata of the installed
	// integrations (Phase C1); their actions are nodes in NodeRegistry.
	// Production installs none yet.
	IntegrationRegistry integration.Registry
	// Knowledge indexes and searches knowledge bases (Phase D2); nil
	// without a knowledge repository.
	Knowledge *knowledge.Service
}

// Dependencies are the runtime collaborators Bootstrap cannot create without
// I/O (Phase 11).
type Dependencies struct {
	// Credentials resolves workspace credentials for provider-backed nodes
	// (credential.Service in production). Nil leaves the LLM node unwired:
	// it then answers with its Phase 4 stub and never calls a provider.
	Credentials credential.Resolver
	// HTTPClient is the shared client of the HTTP-based providers (nil: the
	// provider's default, TLS-verifying client).
	HTTPClient *http.Client
	// Integrations are installed after the built-in nodes: metadata into
	// the integration registry, actions into the node registry (Phase C1).
	// Production passes none; tests install the fake integration here.
	Integrations []integration.Module
	// Gmail, Sheets and Calendar configure the Google API clients (tests
	// point them at mock APIs; the zero value is the real API).
	Gmail    gmail.Options
	Sheets   sheets.Options
	Calendar calendar.Options
	Meet     meet.Options
	Docs     docs.Options
	Forms    forms.Options
	Airtable airtable.Options
	// HTTPTransport replaces the HTTP request node's transport (tests; nil:
	// the private-network-blocking default).
	HTTPTransport http.RoundTripper
	// Files reads workspace files for the file nodes (Phase D1; nil: those
	// nodes fail with a configuration error).
	Files files.Reader
	// KnowledgeRepository stores knowledge bases (Phase D2; nil: the
	// knowledge nodes fail with a configuration error).
	KnowledgeRepository knowledge.Repository
	// Notion configures the Notion API client (Phase C5).
	Notion notion.Options
	// Discord configures the Discord API client (Phase C6). An empty bot
	// token is taken from the configuration (DISCORD_BOT_TOKEN).
	Discord discord.Options
}

// Bootstrap composes the application without runtime dependencies (no
// credential resolution). See BootstrapWith.
func Bootstrap(cfg config.Config) (*Application, error) {
	return BootstrapWith(cfg, Dependencies{})
}

// BootstrapWith is the single startup composition point. It creates the
// provider registry (OpenAI registered, then frozen) and the node registry,
// constructs all canonical V1 nodes with their dependencies, registers them
// with their metadata definitions, and validates registry consistency:
//
//	credential resolver + provider registry -> LLM node -> node registry
//
// The executor sees only the node registry; it knows nothing about providers.
func BootstrapWith(cfg config.Config, deps Dependencies) (*Application, error) {
	// 1. Construct Provider Registry
	// OpenAI, and the free Gemini and Groq APIs (Phase G) through their
	// OpenAI-compatible endpoints.
	providerReg := providerllm.NewRegistry()
	embedders := map[string]providerllm.Embedder{}
	if cfg.Automation.EmbeddingURL != "" {
		embedders["local"] = &compute.Embedder{URL: cfg.Automation.EmbeddingURL, Token: cfg.Automation.EmbeddingToken}
	}
	var transcribers []transcribe.Provider
	for _, p := range []struct {
		spec openai.Spec
		base string
	}{{openai.OpenAI, cfg.OpenAIBaseURL}, {openai.Gemini, cfg.GeminiBaseURL}, {openai.Groq, cfg.GroqBaseURL}} {
		provider, err := openai.NewCompatible(p.spec, openai.Options{BaseURL: p.base, HTTPClient: deps.HTTPClient})
		if err != nil {
			return nil, fmt.Errorf("bootstrap %s provider: %w", p.spec.Name, err)
		}
		if err := providerReg.Register(p.spec.Name, provider); err != nil {
			return nil, fmt.Errorf("bootstrap register provider %q: %w", p.spec.Name, err)
		}
		embedders[p.spec.Name] = provider
		if len(provider.TranscriptionModels()) > 0 {
			transcribers = append(transcribers, transcribe.Provider{Name: p.spec.Name, Label: p.spec.Label, Transcriber: provider})
		}
	}
	providerReg.Freeze()
	var knowledgeSvc *knowledge.Service
	if deps.KnowledgeRepository != nil {
		knowledgeSvc = knowledge.NewService(deps.KnowledgeRepository, deps.Files, deps.Credentials, embedders)
	}
	llmDeps := llm.Dependencies{}
	if deps.Credentials != nil {
		llmDeps = llm.Dependencies{Providers: providerReg, Credentials: deps.Credentials, Files: deps.Files}
	}

	// 2. Construct Node Registry
	nodeReg := node.NewRegistry()

	// 3. Construct all V1 nodes with explicit dependencies injected outside the registry
	type nodePair struct {
		node node.Node
		def  node.NodeDefinition
	}

	nodesToRegister := []nodePair{
		// General category
		{node: input.New(), def: input.Definition()},
		{node: output.New(), def: output.Definition()},
		{node: text.New(), def: text.Definition()},
		{node: nodejson.New(), def: nodejson.Definition()},
		{node: transform.New(), def: transform.Definition()},
		{node: condition.New(), def: condition.Definition()},
		{node: merge.New(), def: merge.Definition()},
		{node: switchnode.New(), def: switchnode.Definition()},

		// AI category
		{node: prompt.New(), def: prompt.Definition()},
		{node: llm.New(llmDeps), def: llm.DefinitionFor(providerllm.Describe(providerReg))},
		{node: transcribe.New(transcribe.Dependencies{Providers: transcribers, Credentials: deps.Credentials, Files: deps.Files}), def: transcribe.Definition(transcribers)},
		{node: structured_output.New(nil), def: structured_output.Definition()},

		// Knowledge category (Phase D)
		{node: extract.New(deps.Files), def: extract.Definition()},
		{node: kb.NewSearch(knowledgeSvc), def: kb.SearchDefinition()},
		{node: kb.NewAdd(knowledgeSvc), def: kb.AddDefinition()},

		// Integration category
		{node: nodehttp.NewWith(nodehttp.Options{Credentials: deps.Credentials, AllowPrivateNetworks: cfg.HTTPAllowPrivateNetworks, Transport: deps.HTTPTransport}), def: nodehttp.Definition()},
	}

	// 4. Register nodes and definitions
	for _, p := range nodesToRegister {
		if err := nodeReg.RegisterNode(p.node, p.def); err != nil {
			return nil, fmt.Errorf("bootstrap register node %q: %w", p.node.Type(), err)
		}
	}

	// 5. Install integrations: their actions are ordinary nodes. Gmail
	// (Phase C3), Google Sheets and Google Calendar (C4), Google Meet, Google Docs and Google Forms (J) are part of the
	// production catalog; they use Google connected accounts, offered only
	// when Google OAuth is configured. Notion (C5), Discord (C6) and Airtable (J) likewise
	// use their connected accounts.
	integrationReg := integration.NewRegistry()
	gmailModule, err := gmail.Module(deps.Credentials, deps.Gmail)
	if err != nil {
		return nil, fmt.Errorf("bootstrap gmail: %w", err)
	}
	sheetsModule, err := sheets.Module(deps.Credentials, deps.Sheets)
	if err != nil {
		return nil, fmt.Errorf("bootstrap google sheets: %w", err)
	}
	calendarModule, err := calendar.Module(deps.Credentials, deps.Calendar)
	if err != nil {
		return nil, fmt.Errorf("bootstrap google calendar: %w", err)
	}
	meetModule, err := meet.Module(deps.Credentials, deps.Meet)
	if err != nil {
		return nil, fmt.Errorf("bootstrap google meet: %w", err)
	}
	docsModule, err := docs.Module(deps.Credentials, deps.Docs)
	if err != nil {
		return nil, fmt.Errorf("bootstrap google docs: %w", err)
	}
	formsModule, err := forms.Module(deps.Credentials, deps.Forms)
	if err != nil {
		return nil, fmt.Errorf("bootstrap google forms: %w", err)
	}
	airtableModule, err := airtable.Module(deps.Credentials, deps.Airtable)
	if err != nil {
		return nil, fmt.Errorf("bootstrap airtable: %w", err)
	}
	notionModule, err := notion.Module(deps.Credentials, deps.Notion)
	if err != nil {
		return nil, fmt.Errorf("bootstrap notion: %w", err)
	}
	if deps.Discord.BotToken.Empty() {
		deps.Discord.BotToken = credential.NewSecret(cfg.DiscordBotToken)
	}
	discordModule, err := discord.Module(deps.Credentials, deps.Discord)
	if err != nil {
		return nil, fmt.Errorf("bootstrap discord: %w", err)
	}
	for _, m := range append([]integration.Module{gmailModule, sheetsModule, calendarModule, meetModule, docsModule, formsModule, airtableModule, notionModule, discordModule}, deps.Integrations...) {
		if err := integration.Install(integrationReg, nodeReg, m); err != nil {
			return nil, fmt.Errorf("bootstrap install integration %q: %w", m.Integration.ID, err)
		}
	}
	integrationReg.Freeze()

	// 6. Validate registry-wide consistency
	if err := nodeReg.Validate(); err != nil {
		return nil, fmt.Errorf("bootstrap registry validation failed: %w", err)
	}

	return &Application{
		Config:              cfg,
		NodeRegistry:        nodeReg,
		ProviderRegistry:    providerReg,
		IntegrationRegistry: integrationReg,
		Knowledge:           knowledgeSvc,
	}, nil
}
