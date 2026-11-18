package email

import "log/slog"

// New selects a Sender implementation per cfg.Backend. Explicit selection
// (not inferred from SMTPHost's presence) avoids a misconfigured/missing
// SMTP setting silently degrading to a no-op mailer in production --
// callers that need that guarantee validate cfg.Backend themselves before
// calling New (see Config.Backend). logger is injected (nil ->
// slog.Default()) for LogSender's own logging.
func New(cfg *Config, logger *slog.Logger) Sender {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Backend == "smtp" {
		s := NewSMTPSender(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, cfg.From)
		s.requireTLS = cfg.SMTPRequireTLS
		return s
	}
	return LogSender{logger: logger}
}
