package agent

import (
	"net"
	"net/http"
	"time"
)

// NewHTTPTransport is the transport used for LLM API traffic.
//
// It deliberately sets no overall request deadline: a streamed answer can run
// for minutes and any fixed Client.Timeout would cut it off mid-sentence.
// The guards are on the phases that should never be slow — connecting, the TLS
// handshake, and the wait for response headers. http.DefaultTransport bounds
// none of these tightly enough, and http.DefaultClient adds no timeout at all,
// so a server that accepted the connection and then went silent hung the agent
// indefinitely.
func NewHTTPTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}
