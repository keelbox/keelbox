package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"

	"github.com/keelbox/keelbox/internal/config"
)

// SendError is a failed send, with nothing from the channel's secrets: no
// URL (Telegram's carries the bot token, a webhook's is the secret), no
// header, no response body. Only the channel, its type, an HTTP status or a
// fixed word for the network problem, and what to check.
type SendError struct {
	Channel, Type string
	Status        int    // HTTP status, 0 for no response
	Problem       string // a fixed phrase, never text from outside
	Fix           string
}

func (e *SendError) Error() string {
	what := e.Problem
	if e.Status != 0 {
		what = fmt.Sprintf("HTTP %d", e.Status)
		if e.Problem != "" {
			what += ": " + e.Problem
		}
	}
	s := fmt.Sprintf("%s (%s): %s", e.Channel, e.Type, what)
	if e.Fix != "" {
		s += ". " + e.Fix
	}
	return s
}

// Retryable reports whether trying again later may work: no response, a
// rate limit or a server error. A refused request needs the owner.
func (e *SendError) Retryable() bool {
	return e.Status == 0 || e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Timeout is the most one send may take.
const Timeout = 10 * time.Second

// newClient is the HTTP client every channel uses: a time limit, no
// redirects, so a server cannot send the request on to a plain-http or
// unexpected host, and, for an address on this machine, a listener that
// allowed accepts (trustedListener).
func newClient(allowed func(uid int) bool) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = trustedListener(allowed)
	return &http.Client{
		Timeout:       Timeout,
		Transport:     t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// checkURL allows https anywhere and plain http only to loopback or the
// tailnet.
func checkURL(u *url.URL) bool {
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		return config.PlainHTTPAllowed(u.Hostname())
	}
	return false
}

// poster sends requests for one channel.
type poster struct {
	channel, kind string
	client        *http.Client
}

func (p poster) fail(status int, problem, fix string) *SendError {
	return &SendError{Channel: p.channel, Type: p.kind, Status: status, Problem: problem, Fix: fix}
}

// post sends body to u. Errors never carry u, the headers or the response.
func (p poster) post(ctx context.Context, u *url.URL, contentType string, body []byte, header http.Header) error {
	if !checkURL(u) {
		return p.fail(0, "the address is not https (plain http only to loopback or the tailnet)", "use an https URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return p.fail(0, "the address is not a valid URL", "check the channel's files")
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "keelbox")
	resp, err := p.client.Do(req)
	if errors.Is(err, errNotSystemListener) {
		return p.fail(0, errNotSystemListener.Error()+" (another account may hold the port while your service is down)",
			"start the service, or run it as a system account; keelbox sends nothing to that port until then")
	}
	if err != nil {
		return p.fail(0, networkProblem(err), "")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return p.fail(resp.StatusCode, statusProblem(resp.StatusCode), "")
}

// networkProblem names what went wrong in fixed words. The error itself is
// never shown: *url.Error carries the URL.
func networkProblem(err error) string {
	var dns *net.DNSError
	var cert x509.UnknownAuthorityError
	var host x509.HostnameError
	var tlsRecord tls.RecordHeaderError
	var timeout interface{ Timeout() bool }
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout():
		return "no answer within 10 s"
	case errors.Is(err, context.Canceled):
		return "stopped"
	case errors.As(err, &dns):
		return "the server's name does not resolve"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH):
		return "no route to the server"
	case errors.As(err, &cert) || errors.As(err, &host) || errors.As(err, &tlsRecord):
		return "the server's TLS certificate is not trusted"
	}
	return "the connection failed"
}

func statusProblem(code int) string {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return "refused: check the channel's credential files"
	case code == http.StatusNotFound:
		return "not found: check the address, topic or chat"
	case code == http.StatusTooManyRequests:
		return "rate limited by the service; retried later"
	case code >= 500:
		return "the service failed; retried later"
	case code >= 400:
		return "the service refused the request"
	}
	return "unexpected answer"
}
