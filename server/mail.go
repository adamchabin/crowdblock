package main

import (
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
)

// SMTP settings (all from the environment; the "register by e-mail" flow is
// disabled unless all four are set):
//
//	SMTP_FROM      sender address, e.g. crowdblock@example.com
//	SMTP_HOST      server address, host or host:port (default port 587)
//	SMTP_USERNAME  login
//	SMTP_PASSWORD  password
//
// The server always authenticates (AUTH PLAIN) and, except to localhost,
// only over STARTTLS: the password is never sent unencrypted.
// shortcut: implicit TLS (port 465) is not supported, use 587 + STARTTLS.
func smtpConfigured() bool { return len(smtpMissing()) == 0 }

// smtpMissing lists the unset SMTP_* variables (names only, never values).
func smtpMissing() (missing []string) {
	for _, v := range []string{"SMTP_FROM", "SMTP_HOST", "SMTP_USERNAME", "SMTP_PASSWORD"} {
		if os.Getenv(v) == "" {
			missing = append(missing, v)
		}
	}
	return
}

// parseEmail accepts a plain address ("user@example.com") only: no display
// name, no CR/LF (header injection), at most 254 characters.
func parseEmail(s string) (string, bool) {
	s = strings.TrimSpace(s)
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || len(s) > 254 || strings.ContainsAny(s, "\r\n<>") {
		return "", false
	}
	return s, true
}

const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no look-alikes

// generatePassword returns 16 random characters (~93 bits).
func generatePassword() (string, error) {
	b := make([]byte, 16)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = passwordAlphabet[n.Int64()]
	}
	return string(b), nil
}

func sendPasswordMail(to, password string) error {
	if !smtpConfigured() {
		return errors.New("SMTP not configured (SMTP_FROM, SMTP_HOST, SMTP_USERNAME, SMTP_PASSWORD)")
	}
	from, addr := os.Getenv("SMTP_FROM"), os.Getenv("SMTP_HOST")
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "587")
	}
	host, _, _ := net.SplitHostPort(addr)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Your crowdblock account\r\n"+
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
		"An account was created for this address.\r\n\r\nEmail:    %s\r\nPassword: %s\r\n\r\n"+
		"Use them to create an API key (POST /api/v1/api-keys).\r\n"+
		"If this wasn't you, ignore this message: nobody can use the account without the password.\r\n",
		from, to, to, password)

	debugf("smtp: sending password mail to %s via %s as %s", to, addr, os.Getenv("SMTP_USERNAME"))
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		return fmt.Errorf("EHLO: %w", err)
	}
	hasTLS, _ := c.Extension("STARTTLS")
	hasAuth, authMechs := c.Extension("AUTH")
	debugf("smtp: connected to %s, STARTTLS offered=%v, AUTH offered (before TLS)=%v %s", addr, hasTLS, hasAuth, authMechs)
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	} else if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return errors.New("server does not offer STARTTLS, refusing to send the password unencrypted")
	}
	if ok, _ := c.Extension("AUTH"); !ok {
		return errors.New("server does not offer AUTH (after STARTTLS)")
	}
	if err := c.Auth(smtp.PlainAuth("", os.Getenv("SMTP_USERNAME"), os.Getenv("SMTP_PASSWORD"), host)); err != nil {
		return fmt.Errorf("AUTH as %s: %w", os.Getenv("SMTP_USERNAME"), err)
	}
	debugf("smtp: authenticated as %s", os.Getenv("SMTP_USERNAME"))
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	debugf("smtp: mail to %s accepted by %s", to, addr)
	return c.Quit()
}
