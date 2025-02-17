package dohutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream"
)

type cacheItem struct {
	mu   sync.RWMutex
	cond *sync.Cond

	ips  []net.IP
	prev *int
	ttl  time.Time
}

func (item *cacheItem) Update(host string, strategy upstream.ForwardStrategy, upstreams []upstream.Upstream) (bool, error) {
	if !item.mu.TryLock() {
		return true, nil
	}

	defer item.mu.Unlock()

	if time.Now().After(item.ttl) {
		ttls := make([]uint32, 0, 8)
		ips := make([]net.IP, 0, 8)
		for _, t := range []uint16{dns.TypeA, dns.TypeAAAA} {
			var msg dns.Msg
			msg.SetQuestion(dns.Fqdn(host), t)
			res, _, err := strategy.ForwardMessage(upstreams, &msg)
			if err != nil {
				return false, fmt.Errorf("failed to forward message: %w", err)
			}

			if res != nil {
				for _, rec := range res.Answer {
					if a, ok := rec.(*dns.A); ok {
						ips = append(ips, a.A)
						ttls = append(ttls, a.Hdr.Ttl)
					} else if aaaa, ok := rec.(*dns.AAAA); ok {
						ips = append(ips, aaaa.AAAA)
						ttls = append(ttls, aaaa.Hdr.Ttl)
					}
				}
			}
		}

		if len(ips) == 0 || len(ttls) == 0 {
			return false, nil
		}

		item.ips = ips
		ttl := uint32(math.MaxUint32)
		for _, t := range ttls {
			if t < ttl {
				ttl = t
			}
		}

		item.ttl = time.Now().Add(time.Duration(ttl) * time.Second)
		item.prev = nil
	}

	return true, nil
}

func CreateHttpClient(strategy upstream.ForwardStrategy, getUpstreams func() []upstream.Upstream) *http.Client {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &*http.DefaultTransport.(*http.Transport)

	var mu sync.RWMutex

	// this cache should only hold a few entries at most so don't worry about clearing expired entries
	cache := make(map[string]*cacheItem)
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		upstreams := getUpstreams()
		if len(upstreams) < 1 {
			return dialer.DialContext(ctx, network, addr)
		}

		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("failed to split host and port: %w", err)
		}

		if ip := net.ParseIP(host); ip != nil {
			// host is already an IP address
			return dialer.DialContext(ctx, network, addr)
		}

		mu.RLock()
		item, ok := cache[host]
		mu.RUnlock()
		if !ok {
			mu.Lock()
			// another thread may have updated the cache while waiting
			// for the lock so check again just in case
			if _, ok := cache[host]; !ok {
				item = &cacheItem{}
				item.cond = sync.NewCond(item.mu.RLocker())
				cache[host] = item
			}
			mu.Unlock()
		}

		item.mu.RLock()
		for time.Now().After(item.ttl) {
			item.mu.RUnlock()

			ok, err := item.Update(host, strategy, upstreams)
			if err != nil {
				return nil, fmt.Errorf("failed to update cache: %w", err)
			}

			item.mu.RLock()
			if !ok {
				break
			}
		}

		// make a copy of the IPs to avoid holding the lock while dialing
		ips := item.ips
		prevIndex := item.prev
		item.mu.RUnlock()

		// try each IP address in a random order

		order := make([]int, len(ips))
		for i := range order {
			order[i] = i
		}

		if prevIndex != nil {
			order[0], order[*prevIndex] = order[*prevIndex], order[0]
			rand.Shuffle(len(order)-1, func(i, j int) {
				order[i+1], order[j+1] = order[j+1], order[i+1]
			})
		} else {
			rand.Shuffle(len(order), func(i, j int) {
				order[i], order[j] = order[j], order[i]
			})
		}

		lastErr := fmt.Errorf("no IPs available")
		for _, i := range order {
			ip := ips[i]
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				if prevIndex == nil || *prevIndex != i {
					item.mu.Lock()
					item.prev = new(int)
					*item.prev = i
					item.mu.Unlock()
				}

				return conn, nil
			}

			log.Printf("INFO: dialing %s failed: %v", ip, err)
			lastErr = err
		}

		return nil, lastErr
	}

	return &http.Client{
		Transport: transport,
	}
}

func Exchange(client *http.Client, server string, msg *dns.Msg) (*dns.Msg, error) {
	reqbody, err := msg.Pack()
	if err != nil {
		return nil, fmt.Errorf("failed to pack doh message: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, server, bytes.NewBuffer(reqbody))
	if err != nil {
		return nil, fmt.Errorf("failed to create doh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send doh request: %w", err)
	}

	defer res.Body.Close()
	resbody, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read doh response: %w", err)
	}

	out := &dns.Msg{}
	if err := out.Unpack(resbody); err != nil {
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return nil, fmt.Errorf("bad doh response status code %d: %s", res.StatusCode, string(resbody))
		}

		return nil, fmt.Errorf("failed to unpack doh response: %w", err)
	}

	return out, nil
}

func testServer(hc *http.Client, server string) bool {
	msg := &dns.Msg{}
	msg.SetQuestion("example.com.", dns.TypeANY)
	_, err := Exchange(hc, server, msg)
	return err == nil
}

func ValidateServer(hc *http.Client, server string) (string, error) {
	if u, err := url.Parse(server); err == nil {
		// this is a valid url, try exchanging a message
		if testServer(hc, server) {
			return server, nil
		}

		if u.Path == "" || u.Path == "/" {
			// maybe it's missing the path, try adding /dns-query
			u.Path = "/dns-query"
			if testServer(hc, u.String()) {
				return u.String(), nil
			}
		}
	}

	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		if testServer(hc, "http://"+server) {
			return "http://" + server, nil
		}

		if testServer(hc, "https://"+server) {
			return "https://" + server, nil
		}

		if u, err := url.Parse("https://" + server); err == nil {
			if u.Path == "" || u.Path == "/" {

				// maybe it's missing the path, try adding /dns-query
				u.Path = "/dns-query"
				if testServer(hc, u.String()) {
					return u.String(), nil
				}

				u.Scheme = "http"
				if testServer(hc, u.String()) {
					return u.String(), nil
				}
			}
		}
	}

	// TODO try checking the svcb record
	return "", fmt.Errorf("invalid doh server %s", server)
}
