package vault

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteCapture appends text to a daily file in the inbox directory.
// Uses a timestamped filename to ensure idempotency and sortability.
// This is the hot path — no blocking operations, no network, no AI.
// Returns the absolute path of the written file.
func WriteCapture(inboxDir, text string) (string, error) {
	if err := os.MkdirAll(inboxDir, 0o755); err != nil {
		return "", fmt.Errorf("create inbox: %w", err)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("capture text is empty")
	}

	now := time.Now()
	content := fmt.Sprintf("%s\n\n%s\n", now.Format("2006-01-02 15:04:05"), text)

	// Filenames are second-granularity, so two captures in the same second
	// would collide — and a plain WriteFile would silently overwrite the
	// earlier one. Create with O_EXCL to detect the collision, then retry
	// with a short random suffix. Still no network, no locks, no AI: the hot
	// path stays fast and, unlike before, loss-free.
	base := now.Format("2006-01-02-150405")
	for attempt := range 1000 {
		name := base + ".md"
		if attempt > 0 {
			name = fmt.Sprintf("%s-%s.md", base, randSuffix())
		}
		path := filepath.Join(inboxDir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create capture: %w", err)
		}
		if _, err := f.WriteString(content); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("write capture: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close capture: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("write capture: too many filename collisions in %s", inboxDir)
}

func randSuffix() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
