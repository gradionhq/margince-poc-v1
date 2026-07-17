// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package mailer is the transactional-email transport (A74/ADR-0056):
// ONE outbound channel for product-originated mail, configured by the
// operator. Its first consumer is password-reset delivery (A107); the
// consent module's marketing lanes are a separate, gated concern and do
// not ride this seam.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Mailer sends one plain-text transactional message. Implementations
// must never log the body: reset mail carries a live credential.
type Mailer interface {
	Send(ctx context.Context, to, subject, textBody string) error
}

// SMTP is the operator-relay transport. STARTTLS is negotiated when the
// relay offers it; credentials are optional (an authenticated submission
// port vs. a local relay).
type SMTP struct {
	Host        string
	Port        int
	Username    string
	Password    string
	FromAddress string
}

// Send submits the message through the configured relay. TLS is
// REQUIRED: a reset mail carries a live credential, so a relay that
// offers no STARTTLS refuses the send instead of downgrading to
// cleartext — except a loopback relay (a local forwarder such as a
// docker mailhog or a host postfix), where the wire never leaves the
// machine.
func (s SMTP) Send(ctx context.Context, to, subject, textBody string) error {
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return errors.New("mailer: recipient and subject must be single-line (header injection)")
	}
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		s.FromAddress, to, subject, textBody)

	client, err := s.connect(ctx, addr)
	if err != nil {
		return err
	}
	defer closeQuietly(client)

	if err := client.Mail(s.FromAddress); err != nil {
		return fmt.Errorf("mailer: relay %s refused the sender: %w", addr, err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("mailer: relay %s refused the recipient: %w", addr, err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("mailer: relay %s refused the message: %w", addr, err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("mailer: relay %s dropped mid-message: %w", addr, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: relay %s did not accept the message: %w", addr, err)
	}
	return client.Quit()
}

// connect dials the relay, bounds the WHOLE exchange with one deadline
// (a stalling relay must not pin a goroutine and socket per reset
// request), secures the channel, and authenticates.
func (s SMTP) connect(ctx context.Context, addr string) (*smtp.Client, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("mailer: relay %s unreachable: %w", addr, err)
	}
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		closeQuietly(conn)
		return nil, fmt.Errorf("mailer: relay %s deadline setup failed: %w", addr, err)
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		closeQuietly(conn)
		return nil, fmt.Errorf("mailer: relay %s greeting failed: %w", addr, err)
	}
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			closeQuietly(client)
			return nil, fmt.Errorf("mailer: relay %s STARTTLS failed: %w", addr, err)
		}
	} else if !isLoopback(s.Host) {
		closeQuietly(client)
		return nil, fmt.Errorf("mailer: relay %s offers no STARTTLS — refusing to send a credential-bearing mail in cleartext", addr)
	}
	if s.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			closeQuietly(client)
			return nil, fmt.Errorf("mailer: relay %s refused the credentials: %w", addr, err)
		}
	}
	return client, nil
}

// isLoopback reports whether the relay lives on this machine — the one
// posture where a missing STARTTLS is acceptable (the bytes never touch
// a network).
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// closeQuietly releases a transport on an error path where the send's
// own error is the one that matters.
func closeQuietly(c io.Closer) {
	//craft:ignore swallowed-errors error-path transport cleanup — the send's own error is already on its way to the caller
	_ = c.Close()
}
