// Package email wraps outbound email sending behind a small
// application-specific interface rather than calling imported/stdlib library
// code inline.
package email

import "context"

type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Config configures New's backend selection. It is deliberately independent
// of any caller's own config type -- callers map their own config into this
// shape at the call site, so this package stays free of any one service's
// config dependency.
type Config struct {
	// Backend selects the Sender implementation: "smtp" or "log". Any value
	// other than "smtp" falls back to LogSender (see New) -- callers that
	// want a hard failure on a mistyped/missing backend should validate
	// Backend themselves before calling New.
	Backend string

	From string

	SMTPHost string
	SMTPPort string
	SMTPUser string
	SMTPPass string
	// SMTPRequireTLS refuses to send unless the server offers STARTTLS.
	// Off by default only because a local relay (MailHog) offers none;
	// anything sending real mail across a network should turn it on.
	SMTPRequireTLS bool
}
