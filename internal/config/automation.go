package config

import "strings"

// AutomationConfig is process configuration; provider secrets never reach browsers.
type AutomationConfig struct {
	TemporalAddress   string
	TemporalNamespace string
	TemporalTaskQueue string
	OpenAIKey         string
	PlannerModel      string
	ElevenLabsKey     string
	ElevenLabsVoice   string
	EmbeddingURL      string
	EmbeddingToken    string
	ConnectorURL      string
	ConnectorToken    string
	WebhookSecret     string
	AnalysisImage     string
}

func loadAutomation(getenv func(string) string) AutomationConfig {
	value := func(key, fallback string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return fallback
	}
	return AutomationConfig{
		TemporalAddress: value("TEMPORAL_ADDRESS", "127.0.0.1:57233"), TemporalNamespace: value("TEMPORAL_NAMESPACE", "default"),
		TemporalTaskQueue: value("TEMPORAL_TASK_QUEUE", "workflow-optimizer-v2"),
		OpenAIKey:         getenv("OPENAI_API_KEY"), PlannerModel: value("PLANNER_MODEL", "gpt-6-luna"),
		ElevenLabsKey: getenv("ELEVENLABS_API_KEY"), ElevenLabsVoice: getenv("ELEVENLABS_VOICE_ID"),
		EmbeddingURL: value("EMBEDDING_URL", "http://127.0.0.1:58081"), EmbeddingToken: getenv("COMPUTE_SERVICE_TOKEN"),
		ConnectorURL: value("WHATSAPP_CONNECTOR_URL", "http://127.0.0.1:58082"), ConnectorToken: getenv("CONNECTOR_SERVICE_TOKEN"),
		WebhookSecret: getenv("AUTOMATION_WEBHOOK_SECRET"), AnalysisImage: value("ANALYSIS_IMAGE", "workflow-optimizer-analysis:local"),
	}
}
