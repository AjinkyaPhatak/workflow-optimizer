package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	temporalworkflow "go.temporal.io/sdk/workflow"
	"workflow-optimizer/internal/application"
	"workflow-optimizer/internal/automation"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/infrastructure/postgres"
	"workflow-optimizer/internal/integration/whatsapp"
	"workflow-optimizer/internal/orchestration"
	"workflow-optimizer/internal/provider/planning"
	"workflow-optimizer/internal/toolbox"
)

func modelKey(ctx context.Context, cfg config.Config, creds credential.Resolver, ws, id uuid.UUID) (string, error) {
	if id != uuid.Nil {
		resolved, err := creds.Resolve(ctx, ws, id, "openai")
		if err != nil {
			return "", &application.InvalidError{Message: "Choose an OpenAI API key from this workspace."}
		}
		return resolved.Secret.Reveal(), nil
	}
	if cfg.Automation.OpenAIKey == "" {
		return "", automation.ErrUnavailable
	}
	return cfg.Automation.OpenAIKey, nil
}
func buildToolbox(cfg config.Config, store *postgres.Store, engine *Application, creds credential.Resolver) *toolbox.Toolbox {
	repo := postgres.NewAutomationRepository(store)
	return &toolbox.Toolbox{Nodes: engine.NodeRegistry, Store: repo, Files: postgres.NewFileRepository(store), Knowledge: engine.Knowledge,
		WhatsApp: &whatsapp.Client{URL: cfg.Automation.ConnectorURL, Token: cfg.Automation.ConnectorToken}, AnalysisImage: cfg.Automation.AnalysisImage,
		Model: func(ctx context.Context, call automation.ToolCall) (toolbox.ModelClient, error) {
			run, err := repo.Run(ctx, call.RunID)
			if err != nil {
				return nil, err
			}
			key, err := modelKey(ctx, cfg, creds, call.WorkspaceID, run.CredentialID)
			if err != nil {
				return nil, err
			}
			return planning.New(key, cfg.OpenAIBaseURL, cfg.Automation.PlannerModel), nil
		},
	}
}
func buildAssistant(cfg config.Config, store *postgres.Store, access *application.Access, creds credential.Resolver, engine *Application) (*application.AssistantService, client.Client, error) {
	var tc client.Client
	var backend automation.Backend
	if cfg.Automation.TemporalAddress != "" {
		var err error
		tc, err = client.NewLazyClient(client.Options{HostPort: cfg.Automation.TemporalAddress, Namespace: cfg.Automation.TemporalNamespace})
		if err != nil {
			return nil, nil, err
		}
		backend = &orchestration.Backend{Client: tc, Queue: cfg.Automation.TemporalTaskQueue}
	}
	tools := buildToolbox(cfg, store, engine, creds)
	svc := &application.AssistantService{Access: access, Store: tools.Store, Backend: backend, Tools: tools.Catalog(),
		Planner: func(ctx context.Context, session automation.Session) (automation.Planner, error) {
			key, err := modelKey(ctx, cfg, creds, session.WorkspaceID, session.CredentialID)
			if err != nil {
				return nil, err
			}
			return planning.New(key, cfg.OpenAIBaseURL, cfg.Automation.PlannerModel), nil
		},
	}
	svc.ToolOptions = func(ctx context.Context, ws uuid.UUID, catalog []automation.ToolDescriptor) ([]automation.ToolDescriptor, error) {
		credentials, err := postgres.NewCredentialRepository(store).ListByWorkspace(ctx, ws)
		if err != nil {
			return nil, err
		}
		out := append([]automation.ToolDescriptor{}, catalog...)
		for i := range out {
			out[i].Inputs = append([]automation.Field{}, out[i].Inputs...)
			for j := range out[i].Inputs {
				field := &out[i].Inputs[j]
				if field.Name != "config.credential_id" {
					continue
				}
				for _, credential := range credentials {
					if credential.Provider == out[i].Provider {
						field.Options = append(field.Options, automation.Option{Label: credential.Name, Value: credential.ID.String()})
					}
				}
			}
		}
		return automation.WithSchemas(out), nil
	}
	return svc, tc, nil
}

// RunOrchestrator is a separate worker process, never an API background goroutine.
func RunOrchestrator(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	enc, err := credentialEncryptor(cfg)
	if err != nil {
		return err
	}
	chain, err := newCredentialChain(cfg, store, enc, Extensions{}, logger)
	if err != nil {
		return err
	}
	deps := chain.dependencies(Extensions{})
	deps.Files = postgres.NewFileRepository(store)
	deps.KnowledgeRepository = postgres.NewKnowledgeRepository(store)
	engine, err := BootstrapWith(cfg, deps)
	if err != nil {
		return err
	}
	tc, err := client.DialContext(ctx, client.Options{HostPort: cfg.Automation.TemporalAddress, Namespace: cfg.Automation.TemporalNamespace})
	if err != nil {
		return fmt.Errorf("connect Temporal: %w", err)
	}
	defer tc.Close()
	queue := cfg.Automation.TemporalTaskQueue
	if queue == "" {
		queue = orchestration.TaskQueue
	}
	w := temporalworker.New(tc, queue, temporalworker.Options{MaxConcurrentActivityExecutionSize: 8})
	w.RegisterWorkflowWithOptions(orchestration.WorkflowV2, temporalworkflow.RegisterOptions{Name: orchestration.WorkflowName})
	w.RegisterWorkflowWithOptions(orchestration.TriggerWorkflow, temporalworkflow.RegisterOptions{Name: orchestration.TriggerWorkflowName})
	w.RegisterWorkflowWithOptions(orchestration.IndexWorkflow, temporalworkflow.RegisterOptions{Name: "knowledge.index"})
	tools := buildToolbox(cfg, store, engine, chain.service)
	bus := &automation.EventBus{Store: tools.Store, Backend: &orchestration.Backend{Client: tc, Queue: queue}}
	activities := &orchestration.Activities{Store: tools.Store, Tools: tools, Events: bus}
	for name, fn := range map[string]any{
		"automation.load": activities.Load, "automation.progress": activities.Progress,
		"automation.execute": activities.Execute, "automation.child": activities.Child, "automation.begin_trigger": activities.BeginTrigger, "automation.compare": activities.Compare,
		"knowledge.index_document": (&orchestration.KnowledgeActivities{Service: engine.Knowledge, Events: bus}).Index,
	} {
		w.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	}
	if err := w.Start(); err != nil {
		return err
	}
	defer w.Stop()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := bus.Drain(ctx); err != nil {
					logger.Warn("automation event delivery is waiting for the execution service")
				}
			}
		}
	}()
	logger.Info("Temporal worker started", "task_queue", orchestration.TaskQueue)
	<-ctx.Done()
	return nil
}
