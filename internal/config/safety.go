// Production safety checks: settings that are fine on a developer's machine
// but dangerous on a reachable host. A process refuses to start with them
// unless APP_URL is a loopback address.

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"

	"github.com/ncecere/open-mcp-gateway/internal/secrets"
)

// exampleKeyHashes are the SHA-256 hashes of the decoded 32-byte keys that
// .env.example ships with. They are public, so an install using them has no
// secret at all. Only hashes are compiled in; a test keeps them in sync with
// .env.example.
var exampleKeyHashes = map[string]string{
	"ENCRYPTION_KEY": "676263bb216f72864f4ebdbeb8eded2b3faf4a4c8ee36fc072a3b6802ad4ca96",
	"API_KEY_PEPPER": "0d81980627611d0f44efd90756c9704a023e58b69683a2a577f78fc3944b63c0",
}

// isExampleKey reports whether value decodes to the example key of setting.
func isExampleKey(setting, value string) bool {
	if value == "" {
		return false
	}
	raw, err := secrets.ParseKey(value)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]) == exampleKeyHashes[setting]
}

// LoopbackAppURL reports whether APP_URL is a loopback origin such as
// http://localhost:8088: a developer's machine, where the safety checks
// do not apply.
func (c Config) LoopbackAppURL() bool {
	u, err := url.Parse(c.AppURL)
	return c.AppURL != "" && err == nil && u.Host != "" && isLoopbackHost(u.Hostname())
}

// AllowPrivateUpstreams is the development allowance for MCP server URLs:
// with DEV_AUTH on a loopback APP_URL, plain http to loopback hosts and
// private, loopback and link-local addresses are accepted. In production,
// internal networks and hosts are allowed only by allowlist entries
// (UPSTREAM_ALLOWED_NETWORKS and Admin › Internal networks), and loopback
// and link-local addresses never.
func (c Config) AllowPrivateUpstreams() bool { return c.DevAuth && c.LoopbackAppURL() }

// SafetyErrors returns the production safety violations for a process mode.
// They are part of Validate; `open-mcp-gateway doctor` lists them too.
//
// The checks apply unless APP_URL is loopback. An empty APP_URL counts as
// production, except for migrate, which needs only DATABASE_URL and is
// checked only when APP_URL is set. A malformed APP_URL is reported by the
// web validation (serve), so it is skipped here.
func (c Config) SafetyErrors(mode string) []error {
	if c.LoopbackAppURL() || (mode == "migrate" && c.AppURL == "") {
		return nil
	}
	var errs []error
	if isExampleKey("ENCRYPTION_KEY", c.EncryptionKey) {
		errs = append(errs, errors.New("ENCRYPTION_KEY is the public example value from .env.example, so stored secrets are not protected. "+
			"Generate a new key with `openssl rand -base64 32`; if secrets were already stored, "+
			"set ENCRYPTION_KEY_PREVIOUS to the old value until they are re-entered"))
	}
	if isExampleKey("API_KEY_PEPPER", c.APIKeyPepper) {
		errs = append(errs, errors.New("API_KEY_PEPPER is the public example value from .env.example, so stored API key digests "+
			"could be checked offline. Generate a new one with `openssl rand -base64 32` (changing it invalidates existing API keys)"))
	}
	if c.DevAuth {
		errs = append(errs, errors.New("DEV_AUTH requires a loopback APP_URL: it lets anyone sign in as a development persona. "+
			"Set DEV_AUTH=false and configure OIDC_ISSUER; never enable it on a reachable host"))
	}
	if u, err := url.Parse(c.AppURL); c.AppURL != "" && err == nil && u.Host != "" && u.Scheme == "http" {
		errs = append(errs, fmt.Errorf("APP_URL must use https unless it is a loopback address (got %s): "+
			"serve Open MCP Gateway behind TLS and set APP_URL=https://%s", u.Scheme+"://"+u.Host, u.Host))
	}
	return errs
}
