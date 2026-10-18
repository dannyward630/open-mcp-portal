package doctor

import (
	"context"
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/config"
	"github.com/ncecere/open-mcp-portal/internal/netpolicy"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

func notifyChecks(t *testing.T, cfg config.Config) map[string]Check {
	t.Helper()
	r := &runner{cfg: cfg, opts: Options{Mode: "serve"}}
	r.notifications(context.Background())
	out := map[string]Check{}
	for _, c := range r.checks {
		if c.Group != "notifications" {
			t.Errorf("unexpected group %q", c.Group)
		}
		out[c.Name] = c
	}
	return out
}

func TestDoctorEmailOffAndWebhooks(t *testing.T) {
	c := notifyChecks(t, config.Defaults())
	if c["email"].Status != Info || !strings.Contains(c["email"].Detail, "off") || c["webhooks"].Status != OK {
		t.Errorf("%+v", c)
	}
	cfg := config.Defaults()
	cfg.NotifyWebhookAllowedNetworks = []netpolicy.Entry{{Kind: netpolicy.KindCIDR, Value: "10.20.0.0/16"}}
	if c := notifyChecks(t, cfg); c["webhooks"].Status != Info || !strings.Contains(c["webhooks"].Detail, "1 internal") {
		t.Errorf("%+v", c["webhooks"])
	}
}

// With SMTP configured, doctor connects (no message is sent) and reports host,
// port, mode and "authenticated", never the username or the password.
func TestDoctorEmailConfigured(t *testing.T) {
	smtp := testutil.NewFakeSMTP(t)
	smtp.Configure(func(f *testutil.FakeSMTP) { f.OfferStartTLS, f.Username, f.Password = false, "mailer", "s3cret-pass" })
	cfg := config.Defaults()
	cfg.SMTP = config.SMTP{Host: smtp.Host, Port: smtp.Port, Username: "mailer", Password: "s3cret-pass", From: "portal@example.com", TLS: "none"}
	c := notifyChecks(t, cfg)["email"]
	if c.Status != Info || !strings.Contains(c.Detail, "unencrypted") || !strings.Contains(c.Detail, "authenticated") {
		t.Errorf("none: %+v", c)
	}
	if strings.Contains(c.Detail+c.Fix, "mailer") || strings.Contains(c.Detail+c.Fix, "s3cret") {
		t.Errorf("the report shows credentials: %+v", c)
	}
	if len(smtp.Messages()) != 0 {
		t.Error("doctor sent a message")
	}
	// STARTTLS mode against a server without STARTTLS fails, naming the class.
	cfg.SMTP.TLS = "starttls"
	cfg.SMTP.Username, cfg.SMTP.Password = "", ""
	if c := notifyChecks(t, cfg)["email"]; c.Status != Fail || !strings.Contains(c.Detail, "tls") {
		t.Errorf("starttls without STARTTLS: %+v", c)
	}
	// Nobody listening.
	cfg.SMTP.Port = 1
	if c := notifyChecks(t, cfg)["email"]; c.Status != Fail || !strings.Contains(c.Detail, "connect") {
		t.Errorf("unreachable: %+v", c)
	}
}
