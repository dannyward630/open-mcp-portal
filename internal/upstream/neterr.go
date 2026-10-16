package upstream

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"syscall"
)

// unreachableMessage describes why a server couldn't be reached, for admins: one
// plain sentence naming the host, never a Go error chain (the class's own
// label, "Couldn't reach the server", is shown beside it, so it isn't
// repeated). The request's URL is only used for its host.
func unreachableMessage(err error) string {
	host := ""
	var ue *url.Error
	if errors.As(err, &ue) {
		if u, perr := url.Parse(ue.URL); perr == nil {
			host = u.Host
		}
	}
	at := ""
	if host != "" {
		at = " " + host
	}
	var dnsErr *net.DNSError
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verify *tls.CertificateVerificationError
	switch {
	case errors.As(err, &dnsErr):
		return fmt.Sprintf("No address was found for %s (DNS lookup failed).", dnsErr.Name)
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Sprintf("The connection to%s was refused: nothing is listening on that port.", at)
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return fmt.Sprintf("There is no network route to%s (host or network unreachable).", at)
	case errors.Is(err, syscall.ECONNRESET):
		return fmt.Sprintf("The connection to%s was reset.", at)
	case errors.As(err, &unknownCA), errors.As(err, &invalid), errors.As(err, &verify):
		return fmt.Sprintf("The certificate of%s isn't trusted.", at)
	case errors.As(err, &hostErr):
		return fmt.Sprintf("The certificate isn't valid for%s.", at)
	case strings.Contains(err.Error(), "server gave HTTP response to HTTPS client"):
		return "The server didn't answer with TLS: does the URL need http:// instead of https://?"
	case strings.Contains(err.Error(), "has no addresses"):
		return fmt.Sprintf("No address was found for%s.", at)
	}
	if host != "" {
		return fmt.Sprintf("The connection to %s failed.", host)
	}
	return "The connection to the server failed."
}
