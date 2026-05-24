package calendar

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"
	"qi/internal/config"
)

func tokenPath(account string) string {
	safe := strings.ReplaceAll(account, string(filepath.Separator), "_")
	return filepath.Join(config.DataDir(), "tokens", safe+".json")
}

func LoadToken(account string) (*oauth2.Token, error) {
	path := tokenPath(account)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("parse token %s: %w", path, err)
	}
	return &tok, nil
}

func SaveToken(account string, tok *oauth2.Token) error {
	path := tokenPath(account)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
