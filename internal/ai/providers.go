// This file is the registry of known providers: name parsing and the
// endpoint/credential presets for the OpenAI-compatible ones. Provider
// construction from config lives in the commands package (buildLLM), which
// resolves env vars; this file only knows the defaults.

package ai

import (
	"fmt"
	"strings"
)

// OpenAICompatPreset is the built-in endpoint and API-key env var for one
// OpenAI-compatible provider. Both are defaults: config may override the
// URL (e.g. Z.AI coding-plan endpoints) and the key env var name.
type OpenAICompatPreset struct {
	BaseURL string
	KeyEnv  string
}

// openAICompatPresets covers every Provider that speaks the OpenAI
// chat-completions wire. Anthropic and Ollama are absent by design — they
// have native implementations.
var openAICompatPresets = map[Provider]OpenAICompatPreset{
	ProviderOpenAI:   {BaseURL: "https://api.openai.com/v1", KeyEnv: "OPENAI_API_KEY"},
	ProviderKimi:     {BaseURL: "https://api.moonshot.ai/v1", KeyEnv: "MOONSHOT_API_KEY"},
	ProviderOpenCode: {BaseURL: "https://opencode.ai/zen/v1", KeyEnv: "OPENCODE_API_KEY"},
	ProviderZAI:      {BaseURL: "https://api.z.ai/api/paas/v4", KeyEnv: "ZAI_API_KEY"},
}

// PresetFor returns the OpenAI-compat preset for p, or ok=false when p has a
// native implementation (or is unknown).
func PresetFor(p Provider) (OpenAICompatPreset, bool) {
	preset, ok := openAICompatPresets[p]
	return preset, ok
}

// KnownProviders lists every accepted provider name, for error messages and
// flag help.
func KnownProviders() []Provider {
	return []Provider{ProviderAnthropic, ProviderOllama, ProviderOpenAI, ProviderKimi, ProviderOpenCode, ProviderZAI}
}

// ParseProvider normalizes a user-supplied provider name. "z.ai" and
// "moonshot" are accepted aliases. An empty string is returned as-is so
// callers keep their own defaulting.
func ParseProvider(s string) (Provider, error) {
	name := strings.ToLower(strings.TrimSpace(s))
	switch name {
	case "":
		return "", nil
	case "z.ai":
		return ProviderZAI, nil
	case "moonshot":
		return ProviderKimi, nil
	}
	p := Provider(name)
	for _, known := range KnownProviders() {
		if p == known {
			return p, nil
		}
	}
	return "", fmt.Errorf("unknown ai provider %q (want %s)", s, providerNames())
}

func providerNames() string {
	names := make([]string, 0, len(KnownProviders()))
	for _, p := range KnownProviders() {
		names = append(names, string(p))
	}
	return strings.Join(names, "|")
}
