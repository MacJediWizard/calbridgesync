package notify

import (
	"context"
	"fmt"
	"net"
	"net/textproto"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// timeoutErr is a net.Error whose Timeout() reports true, standing in
// for an i/o timeout from a deadline-bound SMTP connection.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func TestIsTransientSMTPError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"550 mailbox unavailable is permanent", fmt.Errorf("send email: rcpt to a@b.c: %w", &textproto.Error{Code: 550, Msg: "mailbox unavailable"}), false},
		{"535 auth failed is permanent", fmt.Errorf("send email: auth: %w", &textproto.Error{Code: 535, Msg: "authentication failed"}), false},
		{"421 service not available is transient", fmt.Errorf("send email: %w", &textproto.Error{Code: 421, Msg: "try again later"}), true},
		{"450 mailbox busy is transient", fmt.Errorf("send email: %w", &textproto.Error{Code: 450, Msg: "mailbox busy"}), true},
		{"net timeout is transient", fmt.Errorf("send email: %w", timeoutErr{}), true},
		{"deadline exceeded is transient", fmt.Errorf("send email: %w", os.ErrDeadlineExceeded), true},
		{"unclassified error is transient", fmt.Errorf("send email: connection reset by peer"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientSMTPError(tt.err); got != tt.want {
				t.Errorf("isTransientSMTPError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// fakeSMTPServer runs a minimal plaintext SMTP server (no STARTTLS,
// no AUTH) that answers RCPT with rcptReply. It counts connections and
// records whether a message body was received.
func fakeSMTPServer(t *testing.T, rcptReply string) (port int, conns *atomic.Int32, delivered *atomic.Bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	conns = &atomic.Int32{}
	delivered = &atomic.Bool{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func(c net.Conn) {
				defer c.Close()
				tp := textproto.NewConn(c)
				_ = tp.PrintfLine("220 fake ESMTP")
				for {
					line, err := tp.ReadLine()
					if err != nil {
						return
					}
					cmd := strings.ToUpper(line)
					switch {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						_ = tp.PrintfLine("250 fake")
					case strings.HasPrefix(cmd, "MAIL"):
						_ = tp.PrintfLine("250 ok")
					case strings.HasPrefix(cmd, "RCPT"):
						_ = tp.PrintfLine("%s", rcptReply)
					case strings.HasPrefix(cmd, "DATA"):
						_ = tp.PrintfLine("354 go ahead")
						if _, err := tp.ReadDotLines(); err != nil {
							return
						}
						delivered.Store(true)
						_ = tp.PrintfLine("250 queued")
					case strings.HasPrefix(cmd, "QUIT"):
						_ = tp.PrintfLine("221 bye")
						return
					default:
						_ = tp.PrintfLine("250 ok")
					}
				}
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, conns, delivered
}

func TestSendEmail_PlainDelivers(t *testing.T) {
	port, conns, delivered := fakeSMTPServer(t, "250 ok")
	n := New(&Config{
		EmailEnabled: true,
		SMTPHost:     "127.0.0.1",
		SMTPPort:     port,
		SMTPFrom:     "from@example.com",
	})
	err := n.sendEmail(context.Background(), Alert{Message: "test", Timestamp: time.Now()}, []string{"to@example.com"})
	if err != nil {
		t.Fatalf("sendEmail: %v", err)
	}
	if !delivered.Load() {
		t.Error("server never received the message body")
	}
	if got := conns.Load(); got != 1 {
		t.Errorf("expected 1 connection, got %d", got)
	}
}

// TestSendEmail_PermanentRejectNotRetried verifies a 5xx reply ends
// the send after one attempt instead of burning the retry budget.
func TestSendEmail_PermanentRejectNotRetried(t *testing.T) {
	port, conns, _ := fakeSMTPServer(t, "550 mailbox unavailable")
	n := New(&Config{
		EmailEnabled:    true,
		SMTPHost:        "127.0.0.1",
		SMTPPort:        port,
		SMTPFrom:        "from@example.com",
		MaxSendAttempts: 3,
		InitialBackoff:  time.Millisecond,
	})
	err := n.sendEmail(context.Background(), Alert{Message: "test", Timestamp: time.Now()}, []string{"to@example.com"})
	if err == nil {
		t.Fatal("expected error for 550 reply")
	}
	if got := conns.Load(); got != 1 {
		t.Errorf("expected 1 attempt for permanent 550, got %d", got)
	}
}

// TestSendEmail_TransientRejectRetried verifies a 4xx reply is retried.
func TestSendEmail_TransientRejectRetried(t *testing.T) {
	port, conns, _ := fakeSMTPServer(t, "451 try again later")
	n := New(&Config{
		EmailEnabled:    true,
		SMTPHost:        "127.0.0.1",
		SMTPPort:        port,
		SMTPFrom:        "from@example.com",
		MaxSendAttempts: 3,
		InitialBackoff:  time.Millisecond,
	})
	err := n.sendEmail(context.Background(), Alert{Message: "test", Timestamp: time.Now()}, []string{"to@example.com"})
	if err == nil {
		t.Fatal("expected error for 451 reply")
	}
	if got := conns.Load(); got != 3 {
		t.Errorf("expected 3 attempts for transient 451, got %d", got)
	}
}

// TestSendEmail_HungServerTimesOut verifies that an SMTP server which
// accepts the TCP connection but never sends a greeting does not hang
// sendEmail forever: the per-connection deadline must fire.
func TestSendEmail_HungServerTimesOut(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		t.Run(fmt.Sprintf("tls=%v", useTLS), func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer ln.Close()

			done := make(chan struct{})
			defer close(done)
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					// Hold the connection open without speaking.
					go func(c net.Conn) {
						<-done
						c.Close()
					}(conn)
				}
			}()

			orig := smtpTimeout
			smtpTimeout = 200 * time.Millisecond
			defer func() { smtpTimeout = orig }()

			port := ln.Addr().(*net.TCPAddr).Port
			n := New(&Config{
				EmailEnabled:    true,
				SMTPHost:        "127.0.0.1",
				SMTPPort:        port,
				SMTPFrom:        "from@example.com",
				SMTPTLS:         useTLS,
				MaxSendAttempts: 1,
			})

			errCh := make(chan error, 1)
			go func() {
				errCh <- n.sendEmail(context.Background(), Alert{Message: "test", Timestamp: time.Now()}, []string{"to@example.com"})
			}()

			select {
			case err := <-errCh:
				if err == nil {
					t.Fatal("expected a timeout error, got nil")
				}
				if !isTransientSMTPError(err) {
					t.Errorf("timeout error should be transient: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("sendEmail hung on a server that never responds")
			}
		})
	}
}
