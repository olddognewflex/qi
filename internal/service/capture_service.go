package service

import "qi/internal/vault"

type CaptureService struct {
	InboxPath string
}

func (s CaptureService) Capture(text string) error {
	_, err := vault.WriteCapture(s.InboxPath, text)
	return err
}
