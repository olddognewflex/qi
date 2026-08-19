package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProviderStateOmitsSecrets(t *testing.T) {
	// Given
	apiKey := "sk-test-secret"
	bearer := "Bearer very-secret-token"
	customURL := "https://user:pass@private.example.test/v1"
	envValue := "private-env-value"
	state := ProviderState{
		Version: ProviderStateVersion,
		Entries: []ProviderStateEntry{{
			Provider: ProviderOpenAI,
			Model:    "gpt-test",
			ConfigID: ConfigIDForDescriptor(customURL + "\n" + apiKey + "\n" + bearer + "\n" + envValue),
		}},
	}

	// When
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal provider state: %v", err)
	}

	// Then
	for _, secret := range []string{apiKey, bearer, customURL, envValue} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("serialized provider state contains secret %q: %s", secret, encoded)
		}
	}
	for _, field := range []string{"provider", "model", "config_id", "active"} {
		if !strings.Contains(string(encoded), "\""+field+"\"") {
			t.Errorf("serialized provider state omits %q: %s", field, encoded)
		}
	}
}

func TestProviderStateRejectsInvalid(t *testing.T) {
	validID := ConfigIDForDescriptor("valid")
	tests := []struct {
		name  string
		state ProviderState
	}{
		{name: "empty entries", state: ProviderState{Version: ProviderStateVersion}},
		{name: "unknown provider", state: ProviderState{Version: ProviderStateVersion, Entries: []ProviderStateEntry{{Provider: "unknown", ConfigID: validID}}}},
		{name: "non canonical provider", state: ProviderState{Version: ProviderStateVersion, Entries: []ProviderStateEntry{{Provider: "OpenAI", ConfigID: validID}}}},
		{name: "malformed config id", state: ProviderState{Version: ProviderStateVersion, Entries: []ProviderStateEntry{{Provider: ProviderOpenAI, ConfigID: "ABC"}}}},
		{name: "active below range", state: ProviderState{Version: ProviderStateVersion, Entries: []ProviderStateEntry{{Provider: ProviderOpenAI, ConfigID: validID}}, Active: -1}},
		{name: "active above range", state: ProviderState{Version: ProviderStateVersion, Entries: []ProviderStateEntry{{Provider: ProviderOpenAI, ConfigID: validID}}, Active: 1}},
		{name: "duplicate identity", state: ProviderState{Version: ProviderStateVersion, Entries: []ProviderStateEntry{{Provider: ProviderOpenAI, ConfigID: validID}, {Provider: ProviderOpenAI, ConfigID: validID}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			err := tt.state.Validate()

			// Then
			if err == nil {
				t.Fatal("Validate() error = nil, want invalid state error")
			}
		})
	}
}
