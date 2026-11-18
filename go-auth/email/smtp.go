package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
)

// ErrHeaderInjection means an address carried a line break. Addresses go
// into the message's headers, where a CR or LF would end one header and
// start another of the sender's choosing.
var ErrHeaderInjection = errors.New("email: line break in an address")

// ErrNoTLS means the server did not offer STARTTLS to a sender that
// requires it.
var ErrNoTLS = errors.New("email: server does not offer STARTTLS")

type SMTPSender struct {
	host, port, user, pass, from string
	// requireTLS refuses to send without STARTTLS. net/smtp upgrades
	// opportunistically -- only if the server advertises it -- so anyone on
	// the path can strip the advertisement and read what follows, which
	// for this package is activation and password-reset links.
	requireTLS bool
	// tlsConfig is for tests, which talk to a server with its own
	// certificate; nil means verify against the host name as usual.
	tlsConfig *tls.Config
}

func NewSMTPSender(host, port, user, pass, from string) *SMTPSender {
	return &SMTPSender{host: host, port: port, user: user, pass: pass, from: from}
}

// Send delivers one plain-text message. The connection honours ctx: a
// server that stops answering costs the caller's deadline, not forever.
func (s *SMTPSender) Send(ctx context.Context, to, subject, body string) error {
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(s.from, "\r\n") {
		return ErrHeaderInjection
	}
	msg := s.message(to, subject, body)

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(s.host, s.port))
	if err != nil {
		return fmt.Errorf("email: dial: %w", err)
	}
	// Tie the whole conversation to ctx, not only the dial.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("email: greeting: %w", err)
	}
	defer func() { _ = c.Close() }()

	if ok, _ := c.Extension("STARTTLS"); ok {
		cfg := s.tlsConfig
		if cfg == nil {
			cfg = &tls.Config{ServerName: s.host}
		}
		if err := c.StartTLS(cfg); err != nil {
			return fmt.Errorf("email: starttls: %w", err)
		}
	} else if s.requireTLS {
		return ErrNoTLS
	}

	// Only authenticate when credentials are configured: no-auth relays
	// (MailHog in local dev) refuse AUTH, and net/smtp's PlainAuth itself
	// refuses to send credentials over an unencrypted, non-localhost
	// connection.
	if s.user != "" {
		if err := c.Auth(smtp.PlainAuth("", s.user, s.pass, s.host)); err != nil {
			return fmt.Errorf("email: auth: %w", err)
		}
	}
	if err := c.Mail(s.from); err != nil {
		return fmt.Errorf("email: mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("email: rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("email: data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("email: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: send via smtp: %w", err)
	}
	return c.Quit()
}

// message builds the headers and body. The subject can carry text a user
// chose (an inviter's display name), so its line breaks are flattened and
// it is MIME-encoded rather than trusted to be a single printable line.
func (s *SMTPSender) message(to, subject, body string) []byte {
	subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)
	return []byte("From: " + s.from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" + body + "\r\n")
}
