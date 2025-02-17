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
	Upstream *UpstreamConfig `yaml:"upstream,omitempty"`
	Filters  []string        `yaml:"filters,omitempty"`
	Rewrites []string        `yaml:"rewrites,omitempty"`
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
			Addr: server + ":53",
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

func (l *LazyUpstream) doinit() {
	l.once.Do(func() {
		if !strings.HasPrefix(strings.TrimSpace(l.server), "#") {
			l.upstream, l.server, l.initerr = l.initFunc(l.server)
		}
	})
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
	Strategy           upstream.ForwardStrategy
	BootstrapUpstreams []upstream.Upstream
	Upstreams          [][]upstream.Upstream
}

func (c *Config) Initialize() (*InitializedConfig, error) {
	var hc *http.Client

	var strategy upstream.ForwardStrategy
	if c.Upstream.Strategy != nil && c.Upstream.Strategy.Valid() {
		c.Upstream.Strategy.New()
	} else {
		strategy = upstream.DefaultStrategy
	}

	var upstreams []upstream.Upstream
	var fallback []upstream.Upstream
	var bootstrap []upstream.Upstream
	if c.Upstream != nil {
		bootstrap = make([]upstream.Upstream, len(c.Upstream.Bootstrap))
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

		upstreams = make([]upstream.Upstream, len(c.Upstream.Servers))
		for i, server := range c.Upstream.Servers {
			upstreams[i] = &LazyUpstream{
				server:   server,
				initFunc: initFunc,
			}
		}

		fallback = make([]upstream.Upstream, len(c.Upstream.Fallback))
		for i, server := range c.Upstream.Fallback {
			fallback[i] = &LazyUpstream{
				server:   server,
				initFunc: initFunc,
			}
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

	rewriteUpstream := rewrite.NewRewriteUpstream(c.Rewrites)

	var finalUpstreams [][]upstream.Upstream
	if !rewriteUpstream.Empty() {
		finalUpstreams = append(finalUpstreams, []upstream.Upstream{
			rewriteUpstream,
		})
	}

	if len(filters) > 0 {
		finalUpstreams = append(finalUpstreams, filters)
	}

	if len(upstreams) > 0 {
		finalUpstreams = append(finalUpstreams, upstreams)
	}

	if len(fallback) > 0 {
		finalUpstreams = append(finalUpstreams, fallback)
	}

	return &InitializedConfig{
		Upstreams:          finalUpstreams,
		Strategy:           strategy,
		BootstrapUpstreams: bootstrap,
	}, nil
}

var dnsIPs = []string{
	"# google",
	"1.1.1.1",
	"1.0.0.1",
	"2606:4700:4700::1111",
	"2606:4700:4700::1001",

	"# google",
	"8.8.8.8",
	"8.8.4.4",
	"2001:4860:4860::8888",
	"2001:4860:4860::8844",

	"# china",
	"# 1.2.4.8",
	"# 210.2.4.8",
	"# 240c::6666",
	"# 240c::6644",
}

var DefaultConfig = &Config{
	Upstream: &UpstreamConfig{
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
