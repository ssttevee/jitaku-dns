package config

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/ssttevee/jitaku-dns/internal"
	"github.com/ssttevee/jitaku-dns/internal/dns/dohutil"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream/doh"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream/filter"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream/pool"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream/rewrite"
	"gopkg.in/yaml.v3"
)

type UpstreamConfig struct {
	Strategy *upstream.ForwardStrategyKind `yaml:"strategy,omitempty"`
	Servers  []string                      `yaml:"servers,omitempty"`

	Fallback  []string `yaml:"fallback,omitempty"`
	Bootstrap []string `yaml:"bootstrap,omitempty"`
}

type Config struct {
	Upstream UpstreamConfig `yaml:"upstream,omitempty"`
	Filters  []string       `yaml:"filters,omitempty"`
	Rewrites []string       `yaml:"rewrites,omitempty"`
}

func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) Serialize() []byte {
	s, err := yaml.Marshal(c)
	if err != nil {
		panic(err)
	}

	return []byte(s)
}

var ErrMissingBootstrapServers = errors.New("bootstrap servers required for DoH")

func serverToUpstream(hc *http.Client, server string) (upstream.Upstream, string, error) {
	if u, _ := url.Parse(server); u != nil {
		if u.Scheme == "tcp" || u.Scheme == "udp" {
			host := u.Host
			if u.Port() != "" {
				host += net.JoinHostPort(u.Host, "53")
			}

			return &pool.ConnPoolUpstream{
				Net:  u.Scheme,
				Addr: host,
			}, u.Scheme + "://" + u.Host, nil
		}

		if u.Scheme == "https" || u.Scheme == "http" {
			if hc == nil {
				return nil, "", ErrMissingBootstrapServers
			}

			if validatedServer, _ := dohutil.ValidateServer(hc, server); validatedServer != "" {
				return &doh.DoHUpstream{
					Client: hc,
					URL:    validatedServer,
				}, validatedServer, nil
			}
		}
	}

	// if hc != nil {
	// 	if validatedServer, _ := dohValidateServer(hc, server); validatedServer != "" {
	// 		return &DoHUpstream{
	// 			Client: hc,
	// 			URL:    validatedServer,
	// 		}, validatedServer, nil
	// 	}
	// }

	if host, port, err := net.SplitHostPort(server); err == nil {
		if port != "53" {
			return &pool.ConnPoolUpstream{
				Addr: server,
			}, server, nil
		}

		server = host
	}

	if net.ParseIP(server) != nil {
		return &pool.ConnPoolUpstream{
			Addr: net.JoinHostPort(server, "53"),
		}, server, nil
	}

	return nil, "", fmt.Errorf("invalid server %s", server)
}

func serverToBootstrap(server string) (upstream.Upstream, string, error) {
	upstream, formattedServer, err := serverToUpstream(nil, server)
	if err != nil {
		if errors.Is(err, ErrMissingBootstrapServers) {
			return nil, "", fmt.Errorf("DoH server cannot be used for bootstrap: %s", server)
		}

		return nil, "", err
	}

	return upstream, formattedServer, nil
}

func filterToUpstream(url string) (upstream.Upstream, string, error) {
	f, err := filter.NewFilterUpstream(http.DefaultClient, url)
	if err != nil {
		return nil, "", err
	}

	return f, url, nil
}

type LazyUpstream struct {
	server   string
	initFunc func(server string) (upstream.Upstream, string, error)

	once     sync.Once
	initerr  error
	upstream upstream.Upstream
}

func (l *LazyUpstream) doinit() error {
	l.once.Do(func() {
		s := strings.TrimSpace(l.server)
		if len(s) > 0 && !strings.HasPrefix(s, "#") {
			l.upstream, l.server, l.initerr = l.initFunc(s)
		}
	})

	return l.initerr
}

func (l *LazyUpstream) ConfigLine() string {
	return l.server
}

func (l *LazyUpstream) Inner() upstream.Upstream {
	l.doinit()
	return l.upstream
}

func (l *LazyUpstream) String() string {
	if l.upstream != nil {
		return l.upstream.String()
	}

	return l.server
}

func (l *LazyUpstream) ForwardMessage(msg *dns.Msg) (*dns.Msg, error) {
	l.doinit()
	if l.initerr != nil {
		return nil, l.initerr
	}

	if l.upstream != nil {
		return l.upstream.ForwardMessage(msg)
	}

	return nil, nil
}

func (l *LazyUpstream) Close() error {
	l.doinit()
	if l.upstream != nil {
		defer func() {
			l.upstream = nil
			l.initerr = nil
			l.once = sync.Once{}
		}()

		return l.upstream.Close()
	}

	return nil
}

type InitializedConfig struct {
	Strategy         upstream.ForwardStrategy
	BootstrapServers []upstream.Upstream

	UpstreamServers []upstream.Upstream
	FallbackServers []upstream.Upstream
	Filters         []upstream.Upstream
	Rewrites        upstream.Upstream
}

func (c *InitializedConfig) Validate() error {
	for _, s := range c.BootstrapServers {
		if err := s.(*LazyUpstream).doinit(); err != nil {
			return fmt.Errorf("failed to validate bootstrap server: %w", err)
		}
	}

	for _, s := range c.UpstreamServers {
		if err := s.(*LazyUpstream).doinit(); err != nil {
			return fmt.Errorf("failed to validate upstream server: %w", err)
		}
	}

	for _, s := range c.FallbackServers {
		if err := s.(*LazyUpstream).doinit(); err != nil {
			return fmt.Errorf("failed to validate fallback server: %w", err)
		}
	}

	for _, s := range c.Filters {
		if err := s.(*LazyUpstream).doinit(); err != nil {
			return fmt.Errorf("failed to validate filters: %w", err)
		}
	}

	return nil
}

func (c *InitializedConfig) Config() *Config {
	var strategy *upstream.ForwardStrategyKind
	if c.Strategy != nil {
		strat := c.Strategy.Enum()
		strategy = &strat
	}

	bootstrap := make([]string, len(c.BootstrapServers))
	for i, u := range c.BootstrapServers {
		bootstrap[i] = u.(*LazyUpstream).ConfigLine()
	}

	servers := make([]string, len(c.UpstreamServers))
	for i, u := range c.UpstreamServers {
		servers[i] = u.(*LazyUpstream).ConfigLine()
	}

	fallback := make([]string, len(c.FallbackServers))
	for i, u := range c.FallbackServers {
		fallback[i] = u.(*LazyUpstream).ConfigLine()
	}

	filters := make([]string, len(c.Filters))
	for i, u := range c.Filters {
		filters[i] = u.(*LazyUpstream).ConfigLine()
	}

	var rewrites []string
	if c.Rewrites != nil {
		rewrites = c.Rewrites.(*rewrite.RewriteUpstream).ConfigLines
	}

	return &Config{
		Upstream: UpstreamConfig{
			Strategy:  strategy,
			Servers:   servers,
			Fallback:  fallback,
			Bootstrap: bootstrap,
		},
		Filters:  filters,
		Rewrites: rewrites,
	}
}

func (c *InitializedConfig) forwardMessage(r *dns.Msg) (*dns.Msg, upstream.Upstream, error) {
	if c.Strategy == nil {
		c.Strategy = upstream.DefaultStrategy
	}

	var lastErr error
	if c.Rewrites != nil {
		msg, err := c.Rewrites.ForwardMessage(r)
		if err != nil {
			lastErr = err
		} else if msg != nil {
			return msg, c.Rewrites, nil
		}
	}

	if len(c.Filters) > 0 {
		msg, i, err := c.Strategy.ForwardMessage(c.Filters, r)
		server := c.Filters[i]
		if err != nil {
			lastErr = err
		} else if msg != nil {
			return msg, server, nil
		}
	}

	if len(c.UpstreamServers) > 0 {
		msg, i, err := c.Strategy.ForwardMessage(c.UpstreamServers, r)
		server := c.UpstreamServers[i]
		if err != nil {
			lastErr = err
		} else if msg != nil {
			return msg, server, nil
		}
	}

	if len(c.FallbackServers) > 0 {
		msg, i, err := c.Strategy.ForwardMessage(c.FallbackServers, r)
		server := c.FallbackServers[i]
		if err != nil {
			lastErr = err
		} else if msg != nil {
			return msg, server, nil
		}
	}

	return nil, nil, lastErr
}

func (c *InitializedConfig) ProcessMessage(r *dns.Msg) (*internal.MessageResult, error) {
	start := time.Now()

	res, server, err := c.forwardMessage(r)
	if err != nil {
		err = fmt.Errorf("Failed to forward message to upstream: %w", err)
	}

	if res == nil {
		// log.Println("No upstreams available")

		res = &dns.Msg{}
		res.SetRcode(r, dns.RcodeServerFailure)
		res.Extra = append(res.Extra, dns.TypeToRR[dns.TypeTXT]())
	}

	return &internal.MessageResult{
		Response: res,
		Elapsed:  time.Now().Sub(start),
		Upstream: server,
	}, err
}

func (c *Config) Initialize() *InitializedConfig {
	var hc *http.Client

	var strategy upstream.ForwardStrategy
	if c.Upstream.Strategy != nil && c.Upstream.Strategy.Valid() {
		c.Upstream.Strategy.New()
	} else {
		strategy = upstream.DefaultStrategy
	}

	bootstrap := make([]upstream.Upstream, len(c.Upstream.Bootstrap))
	for i, server := range c.Upstream.Bootstrap {
		bootstrap[i] = &LazyUpstream{
			server:   server,
			initFunc: serverToBootstrap,
		}
	}

	if len(bootstrap) > 0 {
		hc = dohutil.CreateHttpClient(upstream.DefaultStrategy, func() []upstream.Upstream {
			return bootstrap
		})

		hc.Timeout = 5 * time.Second
	}

	initFunc := func(url string) (upstream.Upstream, string, error) {
		return serverToUpstream(hc, url)
	}

	upstreams := make([]upstream.Upstream, len(c.Upstream.Servers))
	for i, server := range c.Upstream.Servers {
		upstreams[i] = &LazyUpstream{
			server:   server,
			initFunc: initFunc,
		}
	}

	fallback := make([]upstream.Upstream, len(c.Upstream.Fallback))
	for i, server := range c.Upstream.Fallback {
		fallback[i] = &LazyUpstream{
			server:   server,
			initFunc: initFunc,
		}
	}

	if hc == nil {
		hc = http.DefaultClient
	}

	filters := make([]upstream.Upstream, len(c.Filters))
	for i, filterConfig := range c.Filters {
		filters[i] = &LazyUpstream{
			server:   filterConfig,
			initFunc: filterToUpstream,
		}
	}

	var rewriteUpstream upstream.Upstream
	if rewrites := rewrite.NewRewriteUpstream(c.Rewrites); !rewrites.Empty() {
		rewriteUpstream = rewrites
	}

	return &InitializedConfig{
		Rewrites:         rewriteUpstream,
		Filters:          filters,
		UpstreamServers:  upstreams,
		FallbackServers:  fallback,
		Strategy:         strategy,
		BootstrapServers: bootstrap,
	}
}

var dnsIPs = []string{
	"# cloudflare",
	"1.1.1.1",
	"1.0.0.1",
	"2606:4700:4700::1111",
	"2606:4700:4700::1001",
	"",
	"# google",
	"8.8.8.8",
	"8.8.4.4",
	"2001:4860:4860::8888",
	"2001:4860:4860::8844",
	"",
	"# china",
	"# 1.2.4.8",
	"# 210.2.4.8",
	"# 240c::6666",
	"# 240c::6644",
}

var DefaultConfig = &Config{
	Upstream: UpstreamConfig{
		Servers: []string{
			"https://cloudflare-dns.com/dns-query",
			"https://dns.google/dns-query",
		},
		Fallback:  dnsIPs,
		Bootstrap: dnsIPs,
	},
	Filters: []string{
		"https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts",
		"# https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt",
		"# https://adguardteam.github.io/HostlistsRegistry/assets/filter_2.txt",
	},
}
