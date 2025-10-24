package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Forwarder manages forwarding DNS queries to an upstream server.
type Forwarder struct {
	upstream UpstreamServerConfig
	client   *http.Client
}

// NewForwarder creates a new Forwarder for the given upstream server.
func NewForwarder(upstream UpstreamServerConfig) *Forwarder {
	var client *http.Client
	if upstream.Type == "doh" {
		client = &http.Client{
			Timeout: 5 * time.Second,
		}
	}
	return &Forwarder{
		upstream: upstream,
		client:   client,
	}
}

// Forward forwards the query to the configured upstream server.
func (f *Forwarder) Forward(ctx context.Context, query []byte) ([]byte, error) {
	switch f.upstream.Type {
	case "udp":
		return f.forwardUDP(ctx, query)
	case "doh":
		return f.forwardDoH(ctx, query)
	default:
		return nil, fmt.Errorf("unknown upstream type: %s", f.upstream.Type)
	}
}

func (f *Forwarder) forwardUDP(ctx context.Context, query []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", f.upstream.Address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	_, err = conn.Write(query)
	if err != nil {
		return nil, err
	}

	buffer := make([]byte, 512)
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetReadDeadline(deadline)
	} else {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	}

	n, err := conn.Read(buffer)
	if err != nil {
		return nil, err
	}

	return buffer[:n], nil
}

func (f *Forwarder) forwardDoH(ctx context.Context, query []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", f.upstream.Address, bytes.NewReader(query))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("doh request failed with status: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}
