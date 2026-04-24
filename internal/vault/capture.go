package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteCapture appends text to a daily file in the inbox directory.
// Uses a timestamped filename to ensure idempotency and sortability.
// This is the hot path — no blocking operations, no network, no AI.
func WriteCapture(inboxDir, text string) error {
	if err := os.MkdirAll(inboxDir, 0o755); err != nil {
		return fmt.Errorf("create inbox: %w", err)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("capture text is empty")
	}

	now := time.Now()
	filename := now.Format("2006-01-02-150405") + ".md"
	path := filepath.Join(inboxDir, filename)

	content := fmt.Sprintf("%s\n\n%s\n", now.Format("2006-01-02 15:04:05"), text)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write capture: %w", err)
	}
	return nil
}
