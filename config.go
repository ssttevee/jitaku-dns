package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"
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
	Servers  []string          `toml:"servers"`
	Strategy *EForwardStrategy `toml:"strategy"`

	Fallback  []string `toml:"fallback"`
	Bootstrap []string `toml:"bootstrap"`
}

type Config struct {
	Upstream *UpstreamConfig `toml:"upstream"`
	Filters  []FilterConfig  `toml:"filters"`
	Rewrites []RewriteConfig `toml:"rewrites"`
}

var ErrNoClient = errors.New("no http client")

func serverToUpstream(hc *http.Client, server string) (Upstream, string, error) {
	if u, _ := url.Parse(server); u != nil {
		if u.Scheme == "tcp" || u.Scheme == "udp" {
			host := u.Host
			if u.Port() != "" {
				host += net.JoinHostPort(u.Host, "53")
			}

			return &ConnPoolUpstream{
				Net:  u.Scheme,
				Addr: host,
			}, u.Scheme + "://" + u.Host, nil
		}

		if u.Scheme == "https" || u.Scheme == "http" {
			if hc == nil {
				return nil, "", ErrNoClient
			}

			if validatedServer, _ := dohValidateServer(hc, server); validatedServer != "" {
				return &DoHUpstream{
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
			return &ConnPoolUpstream{
				Addr: server,
			}, server, nil
		}

		server = host
	}

	if net.ParseIP(server) != nil {
		return &ConnPoolUpstream{
			Addr: server + ":53",
		}, server, nil
	}

	return nil, "", fmt.Errorf("invalid server %s", server)
}

type ValidatedConfig struct {
	Upstreams          [][]Upstream
	Strategy           IForwardStrategy
	BootstrapUpstreams []Upstream
}

func (c *Config) ValidateConfig() (*ValidatedConfig, error) {
	var hc *http.Client

	var strategy IForwardStrategy
	if c.Upstream.Strategy != nil && c.Upstream.Strategy.Valid() {
		c.Upstream.Strategy.New()
	} else {
		strategy = &LinearStrategy{}
	}

	var upstreams []Upstream
	var fallback []Upstream
	var bootstrap []Upstream
	if c.Upstream != nil {
		bootstrap = make([]Upstream, len(c.Upstream.Bootstrap))
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
			hc = createDoHClient(&LinearStrategy{}, func() []Upstream {
				return bootstrap
			})

			hc.Timeout = 5 * time.Second
		}

		upstreams = make([]Upstream, len(c.Upstream.Servers))
		for i, server := range c.Upstream.Servers {
			log.Printf("INFO: checking upstream server %s", server)
			if upstream, formattedServer, err := serverToUpstream(hc, server); err != nil {
				return nil, fmt.Errorf("invalid server %s: %w", server, err)
			} else {
				c.Upstream.Servers[i] = formattedServer
				upstreams[i] = upstream
			}
		}

		fallback = make([]Upstream, len(c.Upstream.Fallback))
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

	var filters []Upstream
	for _, filterConfig := range c.Filters {
		if filterConfig.Disabled {
			continue
		}

		log.Printf("INFO: fetching filter from %s", filterConfig.URL)
		filter, err := fetchAndParseFilter(hc, filterConfig.URL)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch filter %s: %w", filterConfig.URL, err)
		}

		filters = append(filters, &FilterUpstream{
			Filter: filter,
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

	var finalUpstreams [][]Upstream
	if len(rewrites4) > 0 || len(rewrites6) > 0 {
		finalUpstreams = append(finalUpstreams, []Upstream{
			&RewriteUpstream{
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

var defaultConfig = &Config{
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
