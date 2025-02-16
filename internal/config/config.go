package config

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ssttevee/jitaku-dns/internal/dns/dohutil"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream/doh"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream/filter"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream/pool"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream/rewrite"
)

type FilterConfig struct {
	Name     string `toml:"name"`
	URL      string `toml:"url"`
	Disabled bool   `toml:"disabled"`
}

type RewriteConfig struct {
	IP   string
	Name string
}

type UpstreamConfig struct {
	Servers  []string                      `toml:"servers"`
	Strategy *upstream.ForwardStrategyKind `toml:"strategy"`

	Fallback  []string `toml:"fallback"`
	Bootstrap []string `toml:"bootstrap"`
}

type Config struct {
	Upstream *UpstreamConfig `toml:"upstream"`
	Filters  []FilterConfig  `toml:"filters"`
	Rewrites []RewriteConfig `toml:"rewrites"`
}

var ErrNoClient = errors.New("no http client")

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
				return nil, "", ErrNoClient
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

type ValidatedConfig struct {
	Upstreams          [][]upstream.Upstream
	Strategy           upstream.ForwardStrategy
	BootstrapUpstreams []upstream.Upstream
}

func (c *Config) ValidateConfig() (*ValidatedConfig, error) {
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
			log.Printf("INFO: checking bootstrap server %s", server)
			if upstream, formattedServer, err := serverToUpstream(nil, server); err != nil {
				if errors.Is(err, ErrNoClient) {
					return nil, fmt.Errorf("doh server cannot be used for bootstrap: %s", server)
				}

				return nil, fmt.Errorf("invalid bootstrap server %s: %w", server, err)
			} else {
				c.Upstream.Bootstrap[i] = formattedServer
				bootstrap[i] = upstream
			}
		}

		if len(bootstrap) > 0 {
			hc = dohutil.CreateHttpClient(upstream.DefaultStrategy, func() []upstream.Upstream {
				return bootstrap
			})

			hc.Timeout = 5 * time.Second
		}

		upstreams = make([]upstream.Upstream, len(c.Upstream.Servers))
		for i, server := range c.Upstream.Servers {
			log.Printf("INFO: checking upstream server %s", server)
			if upstream, formattedServer, err := serverToUpstream(hc, server); err != nil {
				return nil, fmt.Errorf("invalid server %s: %w", server, err)
			} else {
				c.Upstream.Servers[i] = formattedServer
				upstreams[i] = upstream
			}
		}

		fallback = make([]upstream.Upstream, len(c.Upstream.Fallback))
		for i, server := range c.Upstream.Fallback {
			log.Printf("INFO: checking fallback server %s", server)
			if upstream, formattedServer, err := serverToUpstream(hc, server); err != nil {
				return nil, fmt.Errorf("invalid fallback server %s: %w", server, err)
			} else {
				c.Upstream.Fallback[i] = formattedServer
				fallback[i] = upstream
			}
		}
	}

	if hc == nil {
		hc = http.DefaultClient
	}

	var filters []upstream.Upstream
	for _, filterConfig := range c.Filters {
		if filterConfig.Disabled {
			continue
		}

		log.Printf("INFO: fetching filter from %s", filterConfig.URL)
		f, err := filter.FetchAndParseFilter(hc, filterConfig.URL)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch filter %s: %w", filterConfig.URL, err)
		}

		filters = append(filters, &filter.FilterUpstream{
			Filter: f,
		})
	}

	rewrites4 := make(map[string][]net.IP)
	rewrites6 := make(map[string][]net.IP)
	for _, rewriteConfig := range c.Rewrites {
		ip := net.ParseIP(rewriteConfig.IP)
		if ip == nil {
			return nil, fmt.Errorf("invalid IP address %s", rewriteConfig.IP)
		}

		if v4 := ip.To4(); v4 != nil {
			rewrites4[rewriteConfig.Name] = append(rewrites4[rewriteConfig.Name], v4)
		} else {
			rewrites6[rewriteConfig.Name] = append(rewrites6[rewriteConfig.Name], ip)
		}
	}

	var finalUpstreams [][]upstream.Upstream
	if len(rewrites4) > 0 || len(rewrites6) > 0 {
		finalUpstreams = append(finalUpstreams, []upstream.Upstream{
			&rewrite.RewriteUpstream{
				V4: rewrites4,
				V6: rewrites6,
			},
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

	return &ValidatedConfig{
		Upstreams:          finalUpstreams,
		Strategy:           strategy,
		BootstrapUpstreams: bootstrap,
	}, nil
}

var DefaultConfig = &Config{
	Upstream: &UpstreamConfig{
		Servers: []string{
			"https://cloudflare-dns.com/dns-query",
		},
		Fallback: []string{
			"1.1.1.1",
			"1.0.0.1",
			"8.8.8.8",
			"8.8.4.4",
		},
		Bootstrap: []string{
			"1.1.1.1",
			"1.0.0.1",
			"8.8.8.8",
			"8.8.4.4",
		},
	},
	Filters: []FilterConfig{
		{
			Name: "AdGuard DNS filter",
			URL:  "https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt",
		},
		{
			Name:     "AdAway Default Blocklist",
			URL:      "https://adguardteam.github.io/HostlistsRegistry/assets/filter_2.txt",
			Disabled: true,
		},
	},
}
