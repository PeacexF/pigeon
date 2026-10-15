package smtp

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is a minimal SMTP server: enough of RFC 5321 for net/smtp.
type fakeServer struct {
	t         *testing.T
	ln        net.Listener
	cert      tls.Certificate
	implicit  bool              // TLS from the first byte
	startTLS  bool              // offer STARTTLS
	authMechs string            // e.g. "PLAIN LOGIN"; empty: no AUTH
	replies   map[string]string // verb → reply replacing the normal one
	stall     bool              // never send the greeting

	mu    sync.Mutex
	mails []received
}

type received struct {
	from string
	to   []string
	auth string
	data string
}

func newFakeServer(t *testing.T, cert tls.Certificate, configure func(*fakeServer)) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{t: t, ln: ln, cert: cert, startTLS: true, authMechs: "PLAIN LOGIN", replies: map[string]string{}}
	if configure != nil {
		configure(s)
	}
	if s.implicit {
		s.ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}
	t.Cleanup(func() { _ = s.ln.Close() })
	go s.serve()
	return s
}

func (s *fakeServer) addr() string { return s.ln.Addr().String() }

func (s *fakeServer) received() []received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]received(nil), s.mails...)
}

func (s *fakeServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if s.stall {
		_, _ = conn.Read(make([]byte, 1))
		return
	}
	r := bufio.NewReader(conn)
	reply := func(lines ...string) {
		for _, l := range lines {
			_, _ = conn.Write([]byte(l + "\r\n"))
		}
	}
	readLine := func() (string, bool) {
		l, err := r.ReadString('\n')
		return strings.TrimRight(l, "\r\n"), err == nil
	}
	_, isTLS := conn.(*tls.Conn)
	var cur received

	reply("220 fake ESMTP")
	for {
		line, ok := readLine()
		if !ok {
			return
		}
		verb := strings.ToUpper(strings.Fields(line + " x")[0])
		if r, ok := s.replies[verb]; ok {
			reply(r)
			if r[0] == '2' || r[0] == '3' {
				if verb == "DATA" {
					s.readData(readLine)
				}
			}
			continue
		}
		switch verb {
		case "EHLO":
			lines := []string{"250-fake"}
			if s.startTLS && !isTLS {
				lines = append(lines, "250-STARTTLS")
			}
			if s.authMechs != "" {
				lines = append(lines, "250-AUTH "+s.authMechs)
			}
			reply(append(lines, "250 8BITMIME")...)
		case "STARTTLS":
			reply("220 go ahead")
			tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12})
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, r, isTLS = tc, bufio.NewReader(tc), true
		case "AUTH":
			f := strings.Fields(line)
			switch {
			case len(f) == 3 && strings.EqualFold(f[1], "PLAIN"):
				b, _ := base64.StdEncoding.DecodeString(f[2])
				cur.auth = "PLAIN " + strings.ReplaceAll(string(b), "\x00", "|")
			case len(f) == 2 && strings.EqualFold(f[1], "LOGIN"):
				reply("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
				u, _ := readLine()
				reply("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
				p, _ := readLine()
				ub, _ := base64.StdEncoding.DecodeString(u)
				pb, _ := base64.StdEncoding.DecodeString(p)
				cur.auth = "LOGIN " + string(ub) + "|" + string(pb)
			default:
				reply("504 unsupported")
				continue
			}
			reply("235 ok")
		case "MAIL":
			cur.from = between(line, "<", ">")
			reply("250 ok")
		case "RCPT":
			cur.to = append(cur.to, between(line, "<", ">"))
			reply("250 ok")
		case "DATA":
			reply("354 go ahead")
			cur.data = s.readData(readLine)
			s.mu.Lock()
			s.mails = append(s.mails, cur)
			s.mu.Unlock()
			cur = received{auth: cur.auth}
			reply("250 queued")
		case "RSET", "NOOP":
			reply("250 ok")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 unknown command")
		}
	}
}

func (s *fakeServer) readData(readLine func() (string, bool)) string {
	var b strings.Builder
	for {
		l, ok := readLine()
		if !ok || l == "." {
			return b.String()
		}
		b.WriteString(strings.TrimPrefix(l, ".") + "\r\n")
	}
}

func between(s, open, end string) string {
	_, after, _ := strings.Cut(s, open)
	v, _, _ := strings.Cut(after, end)
	return v
}

// selfSigned returns a certificate for 127.0.0.1 and a pool trusting it.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fake smtp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
