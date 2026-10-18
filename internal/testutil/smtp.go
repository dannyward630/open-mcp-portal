package testutil

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// SMTPMessage is one message a FakeSMTP received.
type SMTPMessage struct {
	From string
	To   []string
	Data string
	// TLS: it arrived over STARTTLS; User: the AUTH PLAIN user.
	TLS  bool
	User string
}

// FakeSMTP is a tiny in-test SMTP server on 127.0.0.1: it offers STARTTLS
// (with a self-signed certificate for 127.0.0.1, trusted through Roots) and
// AUTH PLAIN as asked, and keeps the messages it accepts.
type FakeSMTP struct {
	Host string
	Port int
	// Roots trusts the server's certificate.
	Roots *x509.CertPool

	// OfferStartTLS: advertise STARTTLS (default true via NewFakeSMTP).
	// RequireTLS refuses MAIL before STARTTLS; Username and Password, when
	// set, are required (AUTH PLAIN is offered after STARTTLS only when
	// OfferStartTLS, else immediately). RejectRcpt answers RCPT with 550.
	OfferStartTLS bool
	RequireTLS    bool
	Username      string
	Password      string
	RejectRcpt    bool

	ln   net.Listener
	cert tls.Certificate
	mu   sync.Mutex
	msgs []SMTPMessage
	cmds []string
}

// NewFakeSMTP starts a server offering STARTTLS.
func NewFakeSMTP(t testing.TB) *FakeSMTP {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake smtp"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &FakeSMTP{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Roots: roots, OfferStartTLS: true, ln: ln,
		cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}}
	t.Cleanup(func() { _ = ln.Close() })
	go f.serve()
	return f
}

// smtpSettings are a FakeSMTP's behaviours, read once per connection under
// the lock so a test may change them while the server runs.
type smtpSettings struct {
	offerStartTLS, requireTLS, rejectRcpt bool
	username, password                    string
}

// Configure changes the server's behaviour (OfferStartTLS, RequireTLS,
// Username, Password, RejectRcpt) while holding the lock the server reads
// them under. Set them only through it once the server is running:
// NewFakeSMTP has already started accepting connections.
func (f *FakeSMTP) Configure(set func(f *FakeSMTP)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	set(f)
}

func (f *FakeSMTP) settings() smtpSettings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return smtpSettings{f.OfferStartTLS, f.RequireTLS, f.RejectRcpt, f.Username, f.Password}
}

// Messages are the messages received so far.
func (f *FakeSMTP) Messages() []SMTPMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SMTPMessage(nil), f.msgs...)
}

// Commands are the SMTP commands received (verbs only).
func (f *FakeSMTP) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func (f *FakeSMTP) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(c)
	}
}

func (f *FakeSMTP) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	cfg := f.settings()
	var (
		r      = bufio.NewReader(c)
		w      = c
		secure bool
		authed = cfg.username == ""
		user   string
		msg    SMTPMessage
	)
	say := func(s string) { _, _ = w.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		f.mu.Lock()
		f.cmds = append(f.cmds, verb)
		f.mu.Unlock()
		switch verb {
		case "EHLO", "HELO":
			say("250-fake")
			if cfg.offerStartTLS && !secure {
				say("250-STARTTLS")
			}
			if cfg.username != "" && (secure || !cfg.offerStartTLS) {
				say("250-AUTH PLAIN")
			}
			say("250 OK")
		case "STARTTLS":
			if !cfg.offerStartTLS {
				say("502 not offered")
				continue
			}
			say("220 go ahead")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{f.cert}, MinVersion: tls.VersionTLS12})
			if err := tc.Handshake(); err != nil {
				return
			}
			c, w, r, secure = tc, tc, bufio.NewReader(tc), true
		case "AUTH":
			parts := strings.Fields(line)
			if len(parts) == 3 && strings.EqualFold(parts[1], "PLAIN") {
				raw, _ := base64.StdEncoding.DecodeString(parts[2])
				f2 := strings.Split(string(raw), "\x00")
				if len(f2) == 3 && f2[1] == cfg.username && f2[2] == cfg.password {
					authed, user = true, f2[1]
					say("235 ok")
					continue
				}
			}
			say("535 bad credentials")
		case "MAIL":
			if (cfg.requireTLS && !secure) || !authed {
				say("530 Must issue a STARTTLS command first / authenticate")
				continue
			}
			msg = SMTPMessage{From: line[strings.Index(line, ":")+1:], TLS: secure, User: user}
			say("250 ok")
		case "RCPT":
			if cfg.rejectRcpt {
				say("550 no such user")
				continue
			}
			msg.To = append(msg.To, strings.Trim(line[strings.Index(line, ":")+1:], " <>"))
			say("250 ok")
		case "DATA":
			say("354 end with <CRLF>.<CRLF>")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			msg.Data = b.String()
			f.mu.Lock()
			f.msgs = append(f.msgs, msg)
			f.mu.Unlock()
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		case "RSET", "NOOP":
			say("250 ok")
		default:
			say("502 unknown")
		}
	}
}

// Addr is host:port.
func (f *FakeSMTP) Addr() string { return net.JoinHostPort(f.Host, strconv.Itoa(f.Port)) }
