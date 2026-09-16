package commands

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"qi/internal/ai"
	"qi/internal/config"
)

func buildLLM(providerFlag, modelFlag string) (ai.LLM, string, error) {
	cfg, _ := config.Load()
	provider, err := ai.ParseProvider(providerFlag)
	if err != nil {
		return nil, "", err
	}
	if provider == "" {
		provider, err = ai.ParseProvider(os.Getenv("QI_AI_PROVIDER"))
		if err != nil {
			return nil, "", err
		}
	}
	if provider == "" && len(cfg.AI.Providers) > 0 {
		if modelFlag != "" {
			return nil, "", errors.New("--model is ambiguous with an [[ai.providers]] chain; pin a provider with --provider")
		}
		return buildFallbackLLM(cfg)
	}
	if provider == "" {
		provider, err = ai.ParseProvider(cfg.AI.Provider)
		if err != nil {
			return nil, "", err
		}
	}
	if provider == "" {
		provider = ai.ProviderAnthropic
	}
	entry, err := buildEntry(cfg, provider, providerOverridesFor(cfg, provider), modelFlag)
	if err != nil {
		return nil, "", err
	}
	fallback, err := ai.NewFallbackLLM([]ai.FallbackEntry{entry}, nil)
	return fallback, "", err
}

func buildFallbackLLM(cfg config.Config) (ai.LLM, string, error) {
	entries := make([]ai.FallbackEntry, 0, len(cfg.AI.Providers))
	for _, providerConfig := range cfg.AI.Providers {
		provider, err := ai.ParseProvider(providerConfig.Provider)
		if err != nil || provider == "" {
			fmt.Fprintf(os.Stderr, "qi ai: skipping [[ai.providers]] entry: %v\n", err)
			continue
		}
		entry, err := buildEntry(cfg, provider, providerConfig, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "qi ai: skipping provider %s: %v\n", providerConfig.Provider, err)
			continue
		}
		entries = append(entries, entry)
	}
	fallback, err := ai.NewFallbackLLM(entries, fallbackNotice)
	if err != nil {
		return nil, "", fmt.Errorf("no usable [[ai.providers]] entries: %w", err)
	}
	if err := rejectDuplicateEntries(entries); err != nil {
		return nil, "", err
	}
	return fallback, "", nil
}

func buildResumeLLM(
	cfg config.Config,
	saved ai.ProviderState,
	providerFlag, modelFlag string,
	providerChanged, modelChanged bool,
) (*ai.FallbackLLM, error) {
	if providerChanged {
		provider, err := ai.ParseProvider(providerFlag)
		if err != nil {
			return nil, err
		}
		if provider == "" {
			return nil, errors.New("--provider must not be empty")
		}
		entry, err := buildEntry(cfg, provider, providerOverridesFor(cfg, provider), modelFlag)
		if err != nil {
			return nil, err
		}
		return ai.NewFallbackLLM([]ai.FallbackEntry{entry}, fallbackNotice)
	}
	if modelChanged && len(saved.Entries) != 1 {
		return nil, errors.New("--model is ambiguous with a saved provider chain; pin a provider with --provider")
	}
	entries := make([]ai.FallbackEntry, 0, len(saved.Entries))
	for _, savedEntry := range saved.Entries {
		entry, err := reconstructSavedEntry(cfg, savedEntry)
		if err != nil {
			return nil, err
		}
		if modelChanged {
			entry.Model = modelFlag
		}
		entries = append(entries, entry)
	}
	if modelChanged {
		return ai.NewFallbackLLM(entries, fallbackNotice)
	}
	return ai.NewFallbackLLMFromState(entries, saved, fallbackNotice)
}

func reconstructSavedEntry(cfg config.Config, saved ai.ProviderStateEntry) (ai.FallbackEntry, error) {
	candidates := []config.AIProviderConfig{{}}
	for _, candidate := range cfg.AI.Providers {
		provider, err := ai.ParseProvider(candidate.Provider)
		if err == nil && provider == saved.Provider {
			candidates = append(candidates, candidate)
		}
	}
	var match *ai.FallbackEntry
	for _, candidate := range candidates {
		entry, err := buildEntry(cfg, saved.Provider, candidate, saved.Model)
		if err == nil && entry.ConfigID == saved.ConfigID && match == nil {
			match = &entry
		}
	}
	if match == nil {
		return ai.FallbackEntry{}, fmt.Errorf(
			"saved provider %s is unavailable or changed; pass --provider ... to override", saved.Provider,
		)
	}
	return *match, nil
}

func rejectDuplicateEntries(entries []ai.FallbackEntry) error {
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		identity := string(entry.Provider) + "\x00" + entry.Model + "\x00" + entry.ConfigID
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("duplicate configured provider identity for %s", entry.Provider)
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func providerOverridesFor(cfg config.Config, provider ai.Provider) config.AIProviderConfig {
	for _, candidate := range cfg.AI.Providers {
		if parsed, err := ai.ParseProvider(candidate.Provider); err == nil && parsed == provider {
			return candidate
		}
	}
	return config.AIProviderConfig{}
}

func buildEntry(
	cfg config.Config,
	provider ai.Provider,
	providerConfig config.AIProviderConfig,
	modelOverride string,
) (ai.FallbackEntry, error) {
	model := modelOverride
	if model == "" {
		model = providerConfig.Model
	}
	entry := ai.FallbackEntry{Name: string(provider), Provider: provider}
	switch provider {
	case ai.ProviderOllama:
		endpoint := firstNonEmpty(providerConfig.URL, os.Getenv("OLLAMA_URL"), cfg.AI.OllamaURL, ai.DefaultOllamaURL)
		if model == "" {
			model = firstNonEmpty(cfg.AI.OllamaModel, ai.DefaultOllamaModel)
		}
		normalized, err := normalizeProviderURL(endpoint)
		if err != nil {
			return entry, err
		}
		entry.LLM = ai.NewOllamaProvider(normalized, os.Getenv("OLLAMA_API_KEY"), nil)
		entry.Model = model
		entry.ConfigID = ai.ConfigIDForDescriptor(string(provider) + "|" + normalized + "|OLLAMA_API_KEY")
	case ai.ProviderAnthropic:
		if model == "" {
			model = firstNonEmpty(cfg.AI.Model, ai.DefaultAnthropicModel)
		}
		keyEnv := firstNonEmpty(providerConfig.APIKeyEnv, "ANTHROPIC_API_KEY")
		apiKey := os.Getenv(keyEnv)
		if providerConfig.APIKeyEnv != "" && apiKey == "" {
			return entry, fmt.Errorf("%s not set", keyEnv)
		}
		entry.LLM = ai.NewAnthropicProvider(apiKey)
		entry.Model = model
		entry.ConfigID = ai.ConfigIDForDescriptor(string(provider) + "|default|" + keyEnv)
	default:
		preset, ok := ai.PresetFor(provider)
		if !ok {
			return entry, fmt.Errorf("unknown ai provider %q", provider)
		}
		endpoint := firstNonEmpty(providerConfig.URL, preset.BaseURL)
		keyEnv := firstNonEmpty(providerConfig.APIKeyEnv, preset.KeyEnv)
		normalized, err := normalizeProviderURL(endpoint)
		if err != nil {
			return entry, err
		}
		apiKey := os.Getenv(keyEnv)
		if apiKey == "" {
			return entry, fmt.Errorf("%s not set", keyEnv)
		}
		if model == "" {
			return entry, fmt.Errorf("model is required for provider %q", provider)
		}
		entry.LLM = ai.NewOpenAIProvider(string(provider), normalized, apiKey, nil)
		entry.Model = model
		entry.ConfigID = ai.ConfigIDForDescriptor(string(provider) + "|" + normalized + "|" + keyEnv)
	}
	return entry, nil
}

func normalizeProviderURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("provider URL must be an absolute credential-free URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func fallbackNotice(from, to ai.FallbackEntry, err error) {
	fmt.Fprintf(os.Stderr, "qi ai: provider %s failed (%v); falling back to %s\n", from.Name, err, to.Name)
}
