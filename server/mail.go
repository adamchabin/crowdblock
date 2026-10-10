package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
)

// SMTP settings (all from the environment; without SMTP_HOST the
// "register by e-mail" flow is disabled):
//
//	SMTP_FROM      sender address, e.g. crowdblock@example.com
//	SMTP_HOST      server address, host or host:port (default port 587)
//	SMTP_USERNAME  login (optional: no auth when empty)
//	SMTP_PASSWORD  password
//
// net/smtp upgrades to STARTTLS when the server offers it and refuses to
// send the password over an unencrypted non-local connection.
// shortcut: implicit TLS (port 465) is not supported, use 587 + STARTTLS.
func smtpConfigured() bool {
	return os.Getenv("SMTP_HOST") != "" && os.Getenv("SMTP_FROM") != ""
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
	from, host := os.Getenv("SMTP_FROM"), os.Getenv("SMTP_HOST")
	if from == "" || host == "" {
		return errors.New("SMTP not configured")
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "587")
	}
	hostname, _, _ := net.SplitHostPort(host)

	var auth smtp.Auth
	if u := os.Getenv("SMTP_USERNAME"); u != "" {
		auth = smtp.PlainAuth("", u, os.Getenv("SMTP_PASSWORD"), hostname)
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Your crowdblock account\r\n"+
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
		"An account was created for this address.\r\n\r\nEmail:    %s\r\nPassword: %s\r\n\r\n"+
		"Use them to create an API key (POST /api/v1/api-keys).\r\n"+
		"If this wasn't you, ignore this message: nobody can use the account without the password.\r\n",
		from, to, to, password)
	return smtp.SendMail(host, auth, from, []string{to}, []byte(msg))
}
