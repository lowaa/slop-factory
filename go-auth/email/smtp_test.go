package email

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSMTP is just enough of an SMTP server to accept one message and
// record it. It offers no STARTTLS, like a local relay -- or like a real
// server whose advertisement someone on the path has stripped.
type fakeSMTP struct {
	addr     string
	mu       sync.Mutex
	commands []string
	data     string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	f := &fakeSMTP{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	reply := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	reply("220 fake ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		f.mu.Lock()
		f.commands = append(f.commands, cmd)
		f.mu.Unlock()
		switch verb := strings.ToUpper(strings.SplitN(cmd, " ", 2)[0]); verb {
		case "EHLO", "HELO":
			reply("250 fake")
		case "MAIL", "RCPT":
			reply("250 ok")
		case "DATA":
			reply("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			reply("250 queued")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}

func (f *fakeSMTP) sender(t *testing.T, requireTLS bool) *SMTPSender {
	t.Helper()
	host, port, err := net.SplitHostPort(f.addr)
	require.NoError(t, err)
	s := NewSMTPSender(host, port, "", "", "noreply@example.com")
	s.requireTLS = requireTLS
	return s
}

func (f *fakeSMTP) received() (commands []string, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...), f.data
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestSMTPSendsAMessage(t *testing.T) {
	f := newFakeSMTP(t)
	require.NoError(t, f.sender(t, false).Send(ctx(t), "user@example.com", "Hello", "the body"))

	_, data := f.received()
	assert.Contains(t, data, "To: user@example.com\r\n")
	assert.Contains(t, data, "Subject: Hello\r\n")
	assert.Contains(t, data, "the body")
}

// Requiring TLS means refusing a server that does not offer it, before a
// single header of the message is sent.
func TestSMTPRequiringTLSRefusesAPlaintextServer(t *testing.T) {
	f := newFakeSMTP(t)
	err := f.sender(t, true).Send(ctx(t), "user@example.com", "Reset", "https://example.com/reset#token=secret")
	require.ErrorIs(t, err, ErrNoTLS)

	commands, data := f.received()
	assert.Empty(t, data, "nothing was sent")
	for _, c := range commands {
		assert.NotRegexp(t, `(?i)^(MAIL|RCPT|DATA)`, c)
	}
}

// A subject carries text a user chose (an inviter's name). A line break in
// it must not start a header of its own.
func TestSMTPSubjectCannotInjectHeaders(t *testing.T) {
	f := newFakeSMTP(t)
	require.NoError(t, f.sender(t, false).Send(ctx(t), "user@example.com",
		"Mallory\r\nContent-Type: text/html\r\nX-Injected: yes invited you", "body"))

	_, data := f.received()
	headers := strings.SplitN(data, "\r\n\r\n", 2)[0]
	assert.NotContains(t, headers, "\r\nX-Injected:")
	assert.NotContains(t, headers, "\r\nContent-Type: text/html")
}

func TestSMTPRefusesAnAddressWithALineBreak(t *testing.T) {
	f := newFakeSMTP(t)
	err := f.sender(t, false).Send(ctx(t), "user@example.com\r\nBcc: victim@example.com", "Hi", "body")
	require.ErrorIs(t, err, ErrHeaderInjection)
	commands, _ := f.received()
	assert.Empty(t, commands, "refused before connecting")
}

// A server that stops answering costs the caller's deadline, not forever.
func TestSMTPHonoursTheContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			defer func() { _ = conn.Close() }()
			time.Sleep(5 * time.Second) // never greets
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	c, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = NewSMTPSender(host, port, "", "", "noreply@example.com").Send(c, "user@example.com", "Hi", "body")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
}
