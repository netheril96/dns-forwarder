package lib

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/dns/dnsmessage"
)

type UpstreamDNS interface {
	Query(ctx context.Context, query []byte) ([]byte, error)
	String() string
}

type UpstreamUDP struct {
	address string
	dialer  *net.Dialer
}

func NewUpstreamUDP(address string, dialer *net.Dialer) *UpstreamUDP {
	return &UpstreamUDP{
		address: address,
		dialer:  dialer,
	}
}

func (u *UpstreamUDP) Query(ctx context.Context, query []byte) ([]byte, error) {
	conn, err := u.dialer.DialContext(ctx, "udp", u.address)
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

func (u *UpstreamUDP) String() string {
	return "udp://" + u.address
}

type UpstreamDoH struct {
	address string
	client  *http.Client
}

func NewUpstreamDoH(address string, client *http.Client) *UpstreamDoH {
	return &UpstreamDoH{
		address: address,
		client:  client,
	}
}

func (u *UpstreamDoH) Query(ctx context.Context, query []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", u.address, bytes.NewReader(query))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("doh request failed with status: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}

func (u *UpstreamDoH) String() string {
	return u.address
}

// upstreamState holds the state for an individual upstream within UpstreamMultiple.
type upstreamState struct {
	upstream      UpstreamDNS
	disabledUntil time.Time // If not zero, upstream is disabled until this time.
}

// UpstreamMultiple manages multiple UpstreamDNS servers with failover and retry logic.
// It attempts to query upstreams in sequence, disabling a failed upstream for a
// specified retryInterval.
type UpstreamMultiple struct {
	upstreams         []upstreamState
	individualTimeout time.Duration
	retryInterval     time.Duration
	mu                sync.Mutex // Protects access to upstreams' disabledUntil status
	logger            *zap.Logger
}

// NewUpstreamMultiple creates a new UpstreamMultiple instance.
// It takes a slice of UpstreamDNS, a timeout for each individual upstream query,
// and an interval after which a failed upstream can be retried.
func NewUpstreamMultiple(upstreams []UpstreamDNS, individualTimeout, retryInterval time.Duration, logger *zap.Logger) (*UpstreamMultiple, error) {
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("UpstreamMultiple requires at least one upstream")
	}

	states := make([]upstreamState, len(upstreams))
	for i, u := range upstreams {
		states[i] = upstreamState{upstream: u}
	}

	return &UpstreamMultiple{
		upstreams:         states,
		individualTimeout: individualTimeout,
		retryInterval:     retryInterval,
		logger:            logger,
	}, nil
}

// Query attempts to forward the DNS query to one of the configured upstreams.
// It tries upstreams in sequence, skipping disabled ones. If an upstream fails
// or times out, it is marked as disabled for the retryInterval.
func (um *UpstreamMultiple) Query(ctx context.Context, query []byte) ([]byte, error) {
	um.mu.Lock()
	defer um.mu.Unlock()

	var lastErr error
	var availableUpstreams int

	// First pass: check for available upstreams and re-enable if retryInterval passed
	// This ensures that an upstream that was previously disabled gets a chance to be re-enabled
	// before we even attempt to query it.
	for i := range um.upstreams {
		state := &um.upstreams[i]
		if !state.disabledUntil.IsZero() && time.Now().After(state.disabledUntil) {
			state.disabledUntil = time.Time{} // Re-enable
			um.logger.Info("Upstream re-enabled after retry interval", zap.String("upstream", state.upstream.String()))
		}
		if state.disabledUntil.IsZero() {
			availableUpstreams++
		}
	}

	if availableUpstreams == 0 {
		return nil, fmt.Errorf("no upstreams are currently available to query")
	}

	// Second pass: try querying available upstreams
	for i := range um.upstreams {
		state := &um.upstreams[i]

		if !state.disabledUntil.IsZero() {
			// This upstream is still disabled, skip it
			continue
		}

		// Create a child context with the individual timeout for this specific upstream attempt.
		childCtx, cancel := context.WithTimeout(ctx, um.individualTimeout)
		defer cancel() // Ensure the child context is cancelled to release resources

		response, err := state.upstream.Query(childCtx, query)
		if err == nil {
			return response, nil // Success
		}

		// Failure: disable this upstream
		state.disabledUntil = time.Now().Add(um.retryInterval)
		lastErr = fmt.Errorf("upstream %s failed: %w", state.upstream.String(), err)
		um.logger.Warn("Upstream disabled",
			zap.String("upstream", state.upstream.String()),
			zap.Time("disabledUntil", state.disabledUntil),
			zap.Error(err),
		)
	}

	// If we reach here, all currently available upstreams failed during this query attempt.
	if lastErr != nil {
		return nil, fmt.Errorf("all available upstreams failed: %w", lastErr)
	}
	return nil, fmt.Errorf("unexpected error: no response from any upstream")
}

func (um *UpstreamMultiple) String() string {
	return "multiple"
}

type UpstreamHostWrapper struct {
	inner  UpstreamDNS
	hosts  map[dnsmessage.Question][]dnsmessage.Resource
	logger *zap.Logger
}

func NewUpstreamHostWrapper(inner UpstreamDNS, hostsConfig Hosts, logger *zap.Logger) (*UpstreamHostWrapper, error) {
	hosts := make(map[dnsmessage.Question][]dnsmessage.Resource)

	for _, h := range hostsConfig.Predefined {
		name, err := dnsmessage.NewName(h.Domain + ".")
		if err != nil {
			return nil, fmt.Errorf("invalid domain name '%s' in hosts config: %w", h.Domain, err)
		}

		// A records
		if len(h.IPv4) > 0 {
			qA := dnsmessage.Question{
				Name:  name,
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
			}
			var answers []dnsmessage.Resource
			for _, ipStr := range h.IPv4 {
				ip := net.ParseIP(ipStr)
				if ip == nil || ip.To4() == nil {
					return nil, fmt.Errorf("invalid IPv4 address '%s' for domain '%s'", ipStr, h.Domain)
				}
				answers = append(answers, dnsmessage.Resource{
					Header: dnsmessage.ResourceHeader{
						Name:  name,
						Type:  dnsmessage.TypeA,
						Class: dnsmessage.ClassINET,
						TTL:   3600, // 1 hour TTL
					},
					Body: &dnsmessage.AResource{A: [4]byte(ip.To4())},
				})
			}
			hosts[qA] = answers
		}

		// AAAA records
		if len(h.IPv6) > 0 {
			qAAAA := dnsmessage.Question{
				Name:  name,
				Type:  dnsmessage.TypeAAAA,
				Class: dnsmessage.ClassINET,
			}
			var answers []dnsmessage.Resource
			for _, ipStr := range h.IPv6 {
				ip := net.ParseIP(ipStr)
				if ip == nil || ip.To16() == nil {
					return nil, fmt.Errorf("invalid IPv6 address '%s' for domain '%s'", ipStr, h.Domain)
				}
				answers = append(answers, dnsmessage.Resource{
					Header: dnsmessage.ResourceHeader{
						Name:  name,
						Type:  dnsmessage.TypeAAAA,
						Class: dnsmessage.ClassINET,
						TTL:   3600, // 1 hour TTL
					},
					Body: &dnsmessage.AAAAResource{AAAA: [16]byte(ip.To16())},
				})
			}
			hosts[qAAAA] = answers
		}
	}

	return &UpstreamHostWrapper{
		inner:  inner,
		hosts:  hosts,
		logger: logger,
	}, nil
}

func (u *UpstreamHostWrapper) String() string {
	return fmt.Sprintf("host-wrapper(%s)", u.inner.String())
}

func (u *UpstreamHostWrapper) Query(ctx context.Context, query []byte) ([]byte, error) {
	var p dnsmessage.Parser
	header, err := p.Start(query)
	if err != nil {
		// If the query is unparsable, just forward it.
		return u.inner.Query(ctx, query)
	}

	q, err := p.Question()
	if err != nil {
		// If there are no questions, just forward it.
		if err != dnsmessage.ErrSectionDone {
			u.logger.Warn("failed to parse DNS question, forwarding", zap.Error(err))
		}
	} else {
		// RRL, rate limit
		if answers, ok := u.hosts[q]; ok {
			u.logger.Debug("serving from hosts", zap.String("domain", q.Name.String()))

			// The response code should be NoError.
			header.RCode = dnsmessage.RCodeSuccess
			// We are sending an answer.
			header.Response = true

			builder := dnsmessage.NewBuilder(nil, header)
			builder.EnableCompression()
			builder.StartAnswers()
			for _, ans := range answers {
				var err error
				switch ans.Body.(type) {
				case *dnsmessage.AResource:
					err = builder.AResource(ans.Header, *ans.Body.(*dnsmessage.AResource))
				case *dnsmessage.AAAAResource:
					err = builder.AAAAResource(ans.Header, *ans.Body.(*dnsmessage.AAAAResource))
				default:
					err = fmt.Errorf("should not happen")
				}
				if err != nil {
					// This should not happen if we constructed the resource correctly.
					u.logger.Error("failed to add resource to DNS response", zap.Error(err))
					// Fallback to inner to be safe
					return u.inner.Query(ctx, query)
				}
			}
			return builder.Finish()
		}
	}

	// If not in hosts or not a standard query, pass to inner upstream
	return u.inner.Query(ctx, query)
}

func CreateUpstreamDNS(config *Config, logger *zap.Logger) (UpstreamDNS, error) {
	if len(config.UpstreamServers) == 0 {
		// If there are no upstream servers, but there are hosts, we can still serve from hosts.
		if len(config.Hosts.Predefined) > 0 {
			// Create a dummy inner upstream that always returns an error.
			dummy := &dummyUpstream{}
			return NewUpstreamHostWrapper(dummy, config.Hosts, logger)
		}
		return nil, fmt.Errorf("no upstream servers configured and no hosts defined")
	}
	var upstreams []UpstreamDNS
	dialer := &net.Dialer{}
	client := &http.Client{}

	for _, upstreamConfig := range config.UpstreamServers {
		switch upstreamConfig.Type {
		case "udp":
			upstream := NewUpstreamUDP(upstreamConfig.Address, dialer)
			upstreams = append(upstreams, upstream)
		case "doh":
			upstream := NewUpstreamDoH(upstreamConfig.Address, client)
			upstreams = append(upstreams, upstream)
		default:
			return nil, fmt.Errorf("unknown upstream type: %s", upstreamConfig.Type)
		}
	}

	var upstream UpstreamDNS
	var err error

	if len(upstreams) > 1 {
		upstream, err = NewUpstreamMultiple(upstreams, 5*time.Second, 100*time.Second, logger)
		if err != nil {
			return nil, err
		}
	} else {
		upstream = upstreams[0]
	}

	if len(config.Hosts.Predefined) > 0 {
		upstream, err = NewUpstreamHostWrapper(upstream, config.Hosts, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create host wrapper: %w", err)
		}
	}

	return upstream, nil
}

// dummyUpstream is an UpstreamDNS that always returns an error.
// It's used when no upstreams are configured but hosts are, so the host wrapper can function.
type dummyUpstream struct{}

func (d *dummyUpstream) Query(ctx context.Context, query []byte) ([]byte, error) {
	return nil, fmt.Errorf("no upstream servers configured")
}

func (d *dummyUpstream) String() string {
	return "dummy"
}
