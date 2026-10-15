package smtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"html/template"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	netsmtp "net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
)

var testMsg = &pigeon.Message{
	ID: "0192-abc:mail", EventID: "0192-abc", EventType: "alert.disk", Channel: "mail",
	Title: "Disk almost full ✉", Text: "db1 is at 95%\nact now", Data: map[string]any{"host": "db1"},
}

type env struct {
	cert tls.Certificate
	pool *x509.CertPool
}

func newEnv(t *testing.T) env {
	cert, pool := selfSigned(t)
	return env{cert, pool}
}

func (e env) channel(srv *fakeServer, opts ...Option) *Channel {
	base := []Option{WithTLSConfig(&tls.Config{RootCAs: e.pool, MinVersion: tls.VersionTLS12})}
	return New(srv.addr(), "Pigeon <alerts@example.com>", []string{"ops@example.com", "Jo <jo@example.org>"}, append(base, opts...)...)
}

func parse(t *testing.T, data string) *mail.Message {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatalf("not a valid message: %v\n%s", err, data)
	}
	return m
}

func TestSendStartTLSPlainAuth(t *testing.T) {
	e := newEnv(t)
	srv := newFakeServer(t, e.cert, nil)
	if err := e.channel(srv, WithAuth("user", "pw")).Send(t.Context(), testMsg); err != nil {
		t.Fatal(err)
	}
	got := srv.received()
	if len(got) != 1 {
		t.Fatalf("received %d mails", len(got))
	}
	r := got[0]
	if r.from != "alerts@example.com" || strings.Join(r.to, ",") != "ops@example.com,jo@example.org" || r.auth != "PLAIN |user|pw" {
		t.Errorf("envelope = %+v", r)
	}

	m := parse(t, r.data)
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subject != "Disk almost full ✉" {
		t.Errorf("Subject = %q (%v)", subject, err)
	}
	for k, want := range map[string]string{
		"From":         `"Pigeon" <alerts@example.com>`,
		"To":           `<ops@example.com>, "Jo" <jo@example.org>`,
		"Message-Id":   "<0192-abc.mail@example.com>",
		"Content-Type": "text/plain; charset=utf-8",
	} {
		if got := m.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if _, err := m.Header.Date(); err != nil {
		t.Errorf("Date: %v", err)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(m.Body))
	if string(body) != "db1 is at 95%\r\nact now\r\n" { // DATA ends the last line
		t.Errorf("body = %q", body)
	}
}

func TestSendImplicitTLSLoginAuth(t *testing.T) {
	e := newEnv(t)
	srv := newFakeServer(t, e.cert, func(s *fakeServer) { s.implicit, s.startTLS, s.authMechs = true, false, "LOGIN" })
	if err := e.channel(srv, WithSecurity(ImplicitTLS), WithAuth("user", "pw")).Send(t.Context(), testMsg); err != nil {
		t.Fatal(err)
	}
	if got := srv.received(); len(got) != 1 || got[0].auth != "LOGIN user|pw" {
		t.Errorf("received = %+v", got)
	}
}

func TestSendPlaintext(t *testing.T) {
	e := newEnv(t)
	srv := newFakeServer(t, e.cert, func(s *fakeServer) { s.startTLS = false })
	msg := *testMsg
	msg.Title = ""
	if err := e.channel(srv, WithSecurity(Plaintext)).Send(t.Context(), &msg); err != nil {
		t.Fatal(err)
	}
	m := parse(t, srv.received()[0].data)
	if s := m.Header.Get("Subject"); s != "alert.disk" {
		t.Errorf("Subject without title = %q, want the event type", s)
	}
}

func TestSendHTML(t *testing.T) {
	e := newEnv(t)
	srv := newFakeServer(t, e.cert, nil)
	tmpl := template.Must(template.New("").Parse(`<h1>{{.Title}}</h1><p>{{.Data.host}}</p>`))
	msg := *testMsg
	msg.Data = map[string]any{"host": "<script>"}
	if err := e.channel(srv, WithHTML(tmpl)).Send(t.Context(), &msg); err != nil {
		t.Fatal(err)
	}
	m := parse(t, srv.received()[0].data)
	mediaType, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q", m.Header.Get("Content-Type"))
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextPart() // decodes quoted-printable
		if err != nil {
			break
		}
		b, _ := io.ReadAll(p)
		parts = append(parts, p.Header.Get("Content-Type")+": "+string(b))
	}
	want := []string{
		"text/plain; charset=utf-8: db1 is at 95%\r\nact now",
		"text/html; charset=utf-8: <h1>Disk almost full ✉</h1><p>&lt;script&gt;</p>",
	}
	if strings.Join(parts, "\n") != strings.Join(want, "\n") {
		t.Errorf("parts =\n%s\nwant\n%s", strings.Join(parts, "\n"), strings.Join(want, "\n"))
	}
}

func TestSendHTMLTemplateError(t *testing.T) {
	tmpl := template.Must(template.New("").Option("missingkey=error").Parse(`{{.Data.nope.x}}`))
	err := New("127.0.0.1:1", "a@example.com", []string{"b@example.com"}, WithHTML(tmpl)).Send(t.Context(), testMsg)
	if !pigeon.IsPermanent(err) || !strings.Contains(err.Error(), "render HTML") {
		t.Errorf("err = %v, want a permanent render error", err)
	}
}

func TestSubjectCannotInjectHeaders(t *testing.T) {
	e := newEnv(t)
	srv := newFakeServer(t, e.cert, nil)
	msg := *testMsg
	msg.Title = "hi\r\nBcc: victim@example.com\n\nbody"
	if err := e.channel(srv).Send(t.Context(), &msg); err != nil {
		t.Fatal(err)
	}
	m := parse(t, srv.received()[0].data)
	if m.Header.Get("Bcc") != "" {
		t.Error("title injected a Bcc header")
	}
	if s := m.Header.Get("Subject"); s != "hi Bcc: victim@example.com body" {
		t.Errorf("Subject = %q", s)
	}
}

func TestSendErrors(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeServer)
		opts      []Option
		permanent bool
		msg       string
	}{
		{"no STARTTLS", func(s *fakeServer) { s.startTLS = false }, nil, true, "does not offer STARTTLS"},
		{"no auth mechanism", func(s *fakeServer) { s.authMechs = "CRAM-MD5" }, []Option{WithAuth("u", "p")}, true, "no supported AUTH"},
		{"bad credentials", func(s *fakeServer) { s.replies["AUTH"] = "535 5.7.8 authentication failed" }, []Option{WithAuth("u", "p")}, true, "535"},
		{"unknown recipient", func(s *fakeServer) { s.replies["RCPT"] = "550 5.1.1 no such user" }, nil, true, "550"},
		{"greylisted", func(s *fakeServer) { s.replies["RCPT"] = "451 4.7.1 try again later" }, nil, false, "451"},
		{"busy", func(s *fakeServer) { s.replies["MAIL"] = "421 4.3.2 too busy" }, nil, false, "421"},
		{"rejected content", func(s *fakeServer) { s.replies["DATA"] = "554 5.6.0 rejected" }, nil, true, "554"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			srv := newFakeServer(t, e.cert, tt.configure)
			err := e.channel(srv, tt.opts...).Send(t.Context(), testMsg)
			if err == nil {
				t.Fatal("Send succeeded")
			}
			if pigeon.IsPermanent(err) != tt.permanent || !strings.Contains(err.Error(), tt.msg) {
				t.Errorf("err = %v (permanent %v), want %q, permanent %v", err, pigeon.IsPermanent(err), tt.msg, tt.permanent)
			}
			if strings.Contains(err.Error(), "smtp: smtp:") {
				t.Errorf("err repeats its prefix: %v", err)
			}
		})
	}
}

func TestUntrustedCertificateIsPermanent(t *testing.T) {
	e := newEnv(t)
	srv := newFakeServer(t, e.cert, nil)
	err := New(srv.addr(), "a@example.com", []string{"b@example.com"}).Send(t.Context(), testMsg)
	if !pigeon.IsPermanent(err) {
		t.Errorf("err = %v, want permanent", err)
	}
}

func TestConnectionRefusedIsRetryable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	err = New(addr, "a@example.com", []string{"b@example.com"}).Send(t.Context(), testMsg)
	if err == nil || pigeon.IsPermanent(err) {
		t.Errorf("err = %v, want retryable", err)
	}
}

func TestSendHonorsContext(t *testing.T) {
	e := newEnv(t)
	srv := newFakeServer(t, e.cert, func(s *fakeServer) { s.stall = true })
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := e.channel(srv).Send(ctx, testMsg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Send took %v after its deadline", took)
	}

	ctx, cancel = context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	if err := e.channel(srv).Send(ctx, testMsg); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestLoginAuthRefusesPlaintext(t *testing.T) {
	a := &loginAuth{user: "u", password: "p", host: "h"}
	if _, _, err := a.Start(&netsmtp.ServerInfo{Name: "h"}); err == nil {
		t.Error("LOGIN allowed without TLS")
	}
	if _, _, err := a.Start(&netsmtp.ServerInfo{Name: "other", TLS: true}); err == nil {
		t.Error("LOGIN allowed for the wrong host")
	}
	if _, err := a.Next([]byte("Who are you?"), true); err == nil {
		t.Error("unexpected challenge accepted")
	}
}

func TestValidate(t *testing.T) {
	to := []string{"b@example.com"}
	tests := []struct {
		name string
		ch   *Channel
		want string
	}{
		{"ok", New("smtp.example.com:587", "a@example.com", to), ""},
		{"display names", New("smtp.example.com:465", "Alerts <a@example.com>", []string{"Ops <ops@example.com>"}, WithSecurity(ImplicitTLS)), ""},
		{"no port", New("smtp.example.com", "a@example.com", to), "host:port"},
		{"bad from", New("h:25", "not an address", to), "from"},
		{"injected from", New("h:25", "a@example.com\r\nBcc: x@example.com", to), "line break"},
		{"no recipients", New("h:25", "a@example.com", nil), "no recipients"},
		{"bad to", New("h:25", "a@example.com", []string{"x"}), "to:"},
		{"plaintext auth", New("h:25", "a@example.com", to, WithSecurity(Plaintext), WithAuth("u", "p")), "credentials need"},
		{"bad security", New("h:25", "a@example.com", to, WithSecurity(7)), "unknown security"},
		{"bad helo", New("h:25", "a@example.com", to, WithHelo("x\r\nRSET")), "EHLO"},
	}
	for _, tt := range tests {
		err := tt.ch.Validate()
		if (err == nil) != (tt.want == "") || (err != nil && !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s: Validate() = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestMessageIDIsStable(t *testing.T) {
	c := New("h:25", "a@example.com", []string{"b@example.com"})
	from, _ := mail.ParseAddress("a@example.com")
	to := []*mail.Address{{Address: "b@example.com"}}
	weird := &pigeon.Message{EventID: "my id/1", Channel: "mail"}
	first, _ := c.compose(weird, from, to, time.Now())
	second, _ := c.compose(weird, from, to, time.Now().Add(time.Hour))
	id := parse(t, string(first)).Header.Get("Message-Id")
	if id != "<my-id-1.mail@example.com>" || id != parse(t, string(second)).Header.Get("Message-Id") {
		t.Errorf("Message-ID = %q", id)
	}
}
