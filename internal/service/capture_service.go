package service

import "qi/internal/vault"

type CaptureService struct {
	InboxPath string
}

// NewCaptureService builds a CaptureService writing captures into inboxPath.
func NewCaptureService(inboxPath string) CaptureService {
	return CaptureService{InboxPath: inboxPath}
}

func (s CaptureService) Capture(text string) error {
	_, err := vault.WriteCapture(s.InboxPath, text)
	return err
}
