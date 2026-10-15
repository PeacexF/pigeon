// Package smtp delivers pigeon messages as email over SMTP.
//
// The message title is the subject; without one, the event type is. The
// text is the plain-text body. WithHTML adds an HTML alternative rendered
// from an html/template, which escapes event data.
//
// Connections use STARTTLS by default and fail if the server doesn't offer
// it. Use ImplicitTLS for servers that expect TLS from the start (usually
// port 465), and Plaintext only for a relay on the local machine or network.
// Credentials are never sent without TLS. Each message uses a new connection.
//
// Rejections with a 5xx reply, such as an unknown recipient, are permanent;
// 4xx replies and network errors are retried. The Message-ID header is
// derived from the message ID, so it is the same on every retry.
package smtp

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	netsmtp "net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/internal/truncate"
)

// Security selects how the connection is encrypted.
type Security int

// Security modes.
const (
	// StartTLS upgrades a plain connection, usually on port 587, and fails if the server can't.
	StartTLS Security = iota
	// ImplicitTLS speaks TLS from the start, usually on port 465.
	ImplicitTLS
	// Plaintext sends everything unencrypted. Credentials are refused in this mode.
	Plaintext
)

// MaxSubjectLen bounds the subject, in characters.
const MaxSubjectLen = 250

// Channel sends email to a fixed list of recipients. Create it with New.
type Channel struct {
	addr      string
	from      string
	to        []string
	security  Security
	tlsConfig *tls.Config
	user      string
	password  string
	html      *template.Template
	helo      string
}

// Option configures a Channel.
type Option func(*Channel)

// New returns a Channel sending from one address to the others through the
// server at addr ("host:port"). Addresses may include a display name, as in
// "Pigeon <alerts@example.com>".
func New(addr, from string, to []string, opts ...Option) *Channel {
	c := &Channel{addr: addr, from: from, to: to, helo: "localhost"}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithAuth logs in with PLAIN or LOGIN, whichever the server offers.
func WithAuth(user, password string) Option {
	return func(c *Channel) { c.user, c.password = user, password }
}

// WithSecurity selects StartTLS (the default), ImplicitTLS or Plaintext.
func WithSecurity(s Security) Option { return func(c *Channel) { c.security = s } }

// WithTLSConfig replaces the TLS settings, e.g. to trust a private CA.
// ServerName defaults to the host in addr.
func WithTLSConfig(cfg *tls.Config) Option { return func(c *Channel) { c.tlsConfig = cfg } }

// WithHTML adds an HTML body rendered from t with the *pigeon.Message as data,
// e.g. {{.Title}}, {{.Text}} and {{.Data.host}}.
func WithHTML(t *template.Template) Option { return func(c *Channel) { c.html = t } }

// WithHelo sets the name sent in EHLO. The default is "localhost".
func WithHelo(name string) Option { return func(c *Channel) { c.helo = name } }

// Validate reports configuration that can never work.
func (c *Channel) Validate() error {
	var problems []string
	if host, port, err := net.SplitHostPort(c.addr); err != nil || host == "" || port == "" {
		problems = append(problems, fmt.Sprintf("address %q must be host:port", c.addr))
	}
	if _, err := parseAddress(c.from); err != nil {
		problems = append(problems, fmt.Sprintf("from: %v", err))
	}
	if len(c.to) == 0 {
		problems = append(problems, "no recipients")
	}
	for _, to := range c.to {
		if _, err := parseAddress(to); err != nil {
			problems = append(problems, fmt.Sprintf("to: %v", err))
		}
	}
	switch c.security {
	case StartTLS, ImplicitTLS:
	case Plaintext:
		if c.user != "" {
			problems = append(problems, "credentials need StartTLS or ImplicitTLS")
		}
	default:
		problems = append(problems, fmt.Sprintf("unknown security mode %d", c.security))
	}
	if strings.ContainsAny(c.helo, "\r\n") || c.helo == "" {
		problems = append(problems, "invalid EHLO name")
	}
	if len(problems) > 0 {
		return errors.New("smtp: " + strings.Join(problems, "; "))
	}
	return nil
}

func parseAddress(s string) (*mail.Address, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, fmt.Errorf("address %q contains a line break", s)
	}
	a, err := mail.ParseAddress(s)
	if err != nil {
		return nil, fmt.Errorf("address %q: %w", s, err)
	}
	return a, nil
}

// Send emails msg to every recipient.
func (c *Channel) Send(ctx context.Context, msg *pigeon.Message) error {
	from, err := parseAddress(c.from)
	if err != nil {
		return pigeon.Permanent(fmt.Errorf("smtp: %w", err))
	}
	to := make([]*mail.Address, 0, len(c.to))
	for _, s := range c.to {
		a, err := parseAddress(s)
		if err != nil {
			return pigeon.Permanent(fmt.Errorf("smtp: %w", err))
		}
		to = append(to, a)
	}
	data, err := c.compose(msg, from, to, time.Now())
	if err != nil {
		return pigeon.Permanent(fmt.Errorf("smtp: %w", err))
	}
	if err := c.deliver(ctx, from, to, data); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("smtp: %w", ctx.Err())
		}
		// The connection deadline can fire just before ctx reports it.
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return fmt.Errorf("smtp: %w", context.DeadlineExceeded)
		}
		return classify(err)
	}
	return nil
}

func (c *Channel) deliver(ctx context.Context, from *mail.Address, to []*mail.Address, data []byte) error {
	host, _, _ := net.SplitHostPort(c.addr)
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if c.tlsConfig != nil {
		tlsConfig = c.tlsConfig.Clone()
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = host
		}
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return err
	}
	if c.security == ImplicitTLS {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return err
		}
		conn = tc
	}
	// net/smtp has no context support: enforce ctx through the connection.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	client, err := netsmtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = client.Close() }()

	if err := client.Hello(c.helo); err != nil {
		return err
	}
	if c.security == StartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return pigeon.Permanent(errors.New("server does not offer STARTTLS"))
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if c.user != "" {
		ok, mechs := client.Extension("AUTH")
		var auth netsmtp.Auth
		switch {
		case ok && hasWord(mechs, "PLAIN"):
			auth = netsmtp.PlainAuth("", c.user, c.password, host)
		case ok && hasWord(mechs, "LOGIN"):
			auth = &loginAuth{user: c.user, password: c.password, host: host}
		default:
			return pigeon.Permanent(fmt.Errorf("server offers no supported AUTH mechanism (have %q)", mechs))
		}
		if err := client.Auth(auth); err != nil {
			return err
		}
	}

	if err := client.Mail(from.Address); err != nil {
		return err
	}
	for _, a := range to {
		if err := client.Rcpt(a.Address); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	_ = client.Quit() // the message is accepted; a failed goodbye doesn't change that
	return nil
}

func hasWord(list, word string) bool {
	for f := range strings.FieldsSeq(list) {
		if strings.EqualFold(f, word) {
			return true
		}
	}
	return false
}

func classify(err error) error {
	if pigeon.IsPermanent(err) {
		return fmt.Errorf("smtp: %w", err)
	}
	err = fmt.Errorf("smtp: %w", err)
	if reply, ok := errors.AsType[*textproto.Error](err); ok && reply.Code >= 500 {
		return pigeon.Permanent(err)
	}
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return pigeon.Permanent(err)
	}
	return err
}

type loginAuth struct{ user, password, host string }

func (a *loginAuth) Start(server *netsmtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("refusing LOGIN over an unencrypted connection")
	}
	if server.Name != a.host {
		return "", nil, errors.New("wrong host name")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.user), nil
	case "password:":
		return []byte(a.password), nil
	}
	return nil, fmt.Errorf("unexpected LOGIN challenge %q", fromServer)
}

func (c *Channel) compose(msg *pigeon.Message, from *mail.Address, to []*mail.Address, now time.Time) ([]byte, error) {
	subject := msg.Title
	if subject == "" {
		subject = msg.EventType
	}
	// Fields also removes CR and LF, so a template can't inject headers.
	subject = truncate.Runes(strings.Join(strings.Fields(subject), " "), MaxSubjectLen)

	recipients := make([]string, len(to))
	for i, a := range to {
		recipients[i] = a.String()
	}
	domain := from.Address[strings.LastIndexByte(from.Address, '@')+1:]

	var buf bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	header("From", from.String())
	header("To", strings.Join(recipients, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", now.Format(time.RFC1123Z))
	id := strings.Trim(atom(msg.EventID)+"."+atom(msg.Channel), ".")
	if id == "" {
		id = "pigeon"
	}
	header("Message-ID", "<"+id+"@"+domain+">")
	header("MIME-Version", "1.0")

	if c.html == nil {
		header("Content-Type", "text/plain; charset=utf-8")
		header("Content-Transfer-Encoding", "quoted-printable")
		buf.WriteString("\r\n")
		if err := writeQP(&buf, msg.Text); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	var html bytes.Buffer
	if err := c.html.Execute(&html, msg); err != nil {
		return nil, fmt.Errorf("render HTML: %w", err)
	}
	mw := multipart.NewWriter(&buf)
	header("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	buf.WriteString("\r\n")
	for _, part := range []struct{ typ, body string }{{"text/plain", msg.Text}, {"text/html", html.String()}} {
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.typ + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeQP(w, part.body); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeQP(w io.Writer, s string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := io.WriteString(qp, s); err != nil {
		return err
	}
	return qp.Close()
}

// atom makes s safe inside a Message-ID.
func atom(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, s)
}
