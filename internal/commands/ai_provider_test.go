package commands

import (
	"strings"
	"testing"

	"qi/internal/ai"
	"qi/internal/config"
)

func TestBuildEntry_ConfigIDUsesNormalizedEffectiveDescriptor(t *testing.T) {
	// Given
	t.Setenv("OPENAI_API_KEY", "fresh-secret")
	cfg := config.Config{}
	provider := config.AIProviderConfig{Provider: "openai", Model: "gpt-test", URL: "HTTPS://EXAMPLE.COM/v1/"}

	// When
	entry, err := buildEntry(cfg, ai.ProviderOpenAI, provider, "")
	// Then
	if err != nil {
		t.Fatal(err)
	}
	want := ai.ConfigIDForDescriptor("openai|https://example.com/v1|OPENAI_API_KEY")
	if entry.ConfigID != want {
		t.Fatalf("config id = %q, want %q", entry.ConfigID, want)
	}
}

func TestBuildResumeLLM_UsesSavedProviderWhenDefaultsAndEnvChanged(t *testing.T) {
	// Given
	t.Setenv("OPENAI_API_KEY", "fresh-secret")
	t.Setenv("QI_AI_PROVIDER", "anthropic")
	cfg := config.Config{AI: config.AIConfig{
		Provider: "anthropic",
		Providers: []config.AIProviderConfig{{
			Provider: "openai", Model: "saved-model", URL: "https://saved.example/v1",
		}},
	}}
	savedEntry, err := buildEntry(cfg, ai.ProviderOpenAI, cfg.AI.Providers[0], "")
	if err != nil {
		t.Fatal(err)
	}
	saved := ai.ProviderState{Version: ai.ProviderStateVersion, Entries: []ai.ProviderStateEntry{{
		Provider: savedEntry.Provider, Model: savedEntry.Model, ConfigID: savedEntry.ConfigID,
	}}}

	// When
	llm, err := buildResumeLLM(cfg, saved, "", "", false, false)
	// Then
	if err != nil {
		t.Fatal(err)
	}
	state, err := llm.ProviderState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Entries[0] != saved.Entries[0] {
		t.Fatalf("restored = %+v, want %+v", state, saved)
	}
}

func TestBuildResumeLLM_DeduplicatesPresetIdentity(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "fresh-secret")
	cfg := config.Config{AI: config.AIConfig{Providers: []config.AIProviderConfig{{
		Provider: "openai", Model: "saved-model",
	}}}}
	entry, err := buildEntry(cfg, ai.ProviderOpenAI, cfg.AI.Providers[0], "")
	if err != nil {
		t.Fatal(err)
	}
	saved := ai.ProviderState{Version: ai.ProviderStateVersion, Entries: []ai.ProviderStateEntry{{
		Provider: entry.Provider, Model: entry.Model, ConfigID: entry.ConfigID,
	}}}
	llm, err := buildResumeLLM(cfg, saved, "", "", false, false)
	if err != nil {
		t.Fatalf("unchanged preset identity rejected: %v", err)
	}
	state, err := llm.ProviderState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Entries[0] != saved.Entries[0] {
		t.Fatalf("restored = %+v, want %+v", state, saved)
	}
}

func TestBuildResumeLLM_RejectsChangedSavedProvider(t *testing.T) {
	// Given
	t.Setenv("OPENAI_API_KEY", "fresh-secret")
	cfg := config.Config{AI: config.AIConfig{Providers: []config.AIProviderConfig{{
		Provider: "openai", Model: "saved-model", URL: "https://changed.example/v1",
	}}}}
	saved := ai.ProviderState{Version: ai.ProviderStateVersion, Entries: []ai.ProviderStateEntry{{
		Provider: ai.ProviderOpenAI, Model: "saved-model", ConfigID: strings.Repeat("a", 64),
	}}}

	// When
	_, err := buildResumeLLM(cfg, saved, "", "", false, false)

	// Then
	if err == nil || !strings.Contains(err.Error(), "saved provider openai is unavailable or changed; pass --provider ... to override") {
		t.Fatalf("error = %v", err)
	}
}

func TestAIResumeCLIRejectsModelOnlyChain(t *testing.T) {
	// Given
	saved := ai.ProviderState{Version: ai.ProviderStateVersion, Entries: []ai.ProviderStateEntry{
		{Provider: ai.ProviderOllama, Model: "one", ConfigID: strings.Repeat("a", 64)},
		{Provider: ai.ProviderAnthropic, Model: "two", ConfigID: strings.Repeat("b", 64)},
	}}

	// When
	_, err := buildResumeLLM(config.Config{}, saved, "", "override", false, true)

	// Then
	if err == nil || !strings.Contains(err.Error(), "--model is ambiguous") {
		t.Fatalf("error = %v", err)
	}
}

func TestAIResumeCLIKeepsFailedOverProvider(t *testing.T) {
	// Given
	t.Setenv("OPENAI_API_KEY", "fresh-secret")
	cfg := config.Config{AI: config.AIConfig{Providers: []config.AIProviderConfig{
		{Provider: "openai", Model: "primary", URL: "https://primary.example/v1"},
		{Provider: "openai", Model: "backup", URL: "https://backup.example/v1"},
	}}}
	first, err := buildEntry(cfg, ai.ProviderOpenAI, cfg.AI.Providers[0], "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildEntry(cfg, ai.ProviderOpenAI, cfg.AI.Providers[1], "")
	if err != nil {
		t.Fatal(err)
	}
	saved := ai.ProviderState{Version: ai.ProviderStateVersion, Active: 1, Entries: []ai.ProviderStateEntry{
		{Provider: first.Provider, Model: first.Model, ConfigID: first.ConfigID},
		{Provider: second.Provider, Model: second.Model, ConfigID: second.ConfigID},
	}}

	// When
	llm, err := buildResumeLLM(cfg, saved, "", "", false, false)
	// Then
	if err != nil {
		t.Fatal(err)
	}
	state, err := llm.ProviderState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Active != 1 {
		t.Fatalf("active = %d, want 1", state.Active)
	}
}

func TestAIResumeCLIExplicitOverrides(t *testing.T) {
	// Given
	t.Setenv("OPENAI_API_KEY", "fresh-secret")
	cfg := config.Config{AI: config.AIConfig{Providers: []config.AIProviderConfig{{
		Provider: "openai", Model: "saved", URL: "https://saved.example/v1",
	}}}}
	entry, err := buildEntry(cfg, ai.ProviderOpenAI, cfg.AI.Providers[0], "")
	if err != nil {
		t.Fatal(err)
	}
	saved := ai.ProviderState{Version: ai.ProviderStateVersion, Entries: []ai.ProviderStateEntry{{
		Provider: entry.Provider, Model: entry.Model, ConfigID: entry.ConfigID,
	}}}

	// When
	llm, err := buildResumeLLM(cfg, saved, "", "explicit", false, true)
	// Then
	if err != nil {
		t.Fatal(err)
	}
	state, err := llm.ProviderState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Entries[0].Model != "explicit" {
		t.Fatalf("model = %q, want explicit", state.Entries[0].Model)
	}
}
