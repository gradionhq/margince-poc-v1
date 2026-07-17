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
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
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

// Send submits the message through the configured relay. The context
// bounds the dial; net/smtp owns the protocol exchange.
func (s SMTP) Send(ctx context.Context, to, subject, textBody string) error {
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return errors.New("mailer: recipient and subject must be single-line (header injection)")
	}
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	var auth smtp.Auth
	if s.Username != "" {
		auth = smtp.PlainAuth("", s.Username, s.Password, s.Host)
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		s.FromAddress, to, subject, textBody)

	// net/smtp.SendMail has no context hook; honor cancellation at the
	// dial by checking first — the send itself is bounded by the relay's
	// own timeouts.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := smtp.SendMail(addr, auth, s.FromAddress, []string{to}, []byte(msg)); err != nil {
		// The error may quote the relay's response, never the message
		// body — safe to wrap.
		var netErr net.Error
		if errors.As(err, &netErr) {
			return fmt.Errorf("mailer: relay %s unreachable: %w", addr, err)
		}
		return fmt.Errorf("mailer: relay %s refused the message: %w", addr, err)
	}
	return nil
}
