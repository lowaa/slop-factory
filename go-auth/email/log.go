package email

import (
	"context"
	"log/slog"
)

// LogSender logs the full email instead of sending it -- the local-dev
// default, so an activation/password-reset link can be read straight out of
// the service's logs without a real mail server.
type LogSender struct {
	logger *slog.Logger
}

func (s LogSender) Send(ctx context.Context, to, subject, body string) error {
	s.logger.Info("email", "to", to, "subject", subject, "body", body)
	return nil
}
