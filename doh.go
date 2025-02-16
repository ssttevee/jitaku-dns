package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

func createDoHClient(strategy IForwardStrategy, getUpstreams func() []Upstream) *http.Client {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &*http.DefaultTransport.(*http.Transport)

	var mu sync.RWMutex

	// this cache should only hold a few entries at most so don't worry about clearing expired entries
	cache := make(map[string]*CacheItem)
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
				item = &CacheItem{}
				item.cond = sync.NewCond(item.mu.RLocker())
				cache[host] = item
			}
			mu.Unlock()
		}

		item.mu.RLock()
		for time.Now().After(item.ttl) {
			item.mu.RUnlock()
			if err := item.Update(host, strategy, upstreams); err != nil {
				return nil, fmt.Errorf("failed to update cache: %w", err)
			}

			item.mu.RLock()
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

func dohExchange(client *http.Client, server string, msg *dns.Msg) (*dns.Msg, error) {
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

func dohTestServer(hc *http.Client, server string) bool {
	msg := &dns.Msg{}
	msg.SetQuestion("example.com.", dns.TypeANY)
	_, err := dohExchange(hc, server, msg)
	return err == nil
}

func dohValidateServer(hc *http.Client, server string) (string, error) {
	if u, err := url.Parse(server); err == nil {
		// this is a valid url, try exchanging a message
		if dohTestServer(hc, server) {
			return server, nil
		}

		if u.Path == "" || u.Path == "/" {
			// maybe it's missing the path, try adding /dns-query
			u.Path = "/dns-query"
			if dohTestServer(hc, u.String()) {
				return u.String(), nil
			}
		}
	}

	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		if dohTestServer(hc, "http://"+server) {
			return "http://" + server, nil
		}

		if dohTestServer(hc, "https://"+server) {
			return "https://" + server, nil
		}

		if u, err := url.Parse("https://" + server); err == nil {
			if u.Path == "" || u.Path == "/" {

				// maybe it's missing the path, try adding /dns-query
				u.Path = "/dns-query"
				if dohTestServer(hc, u.String()) {
					return u.String(), nil
				}

				u.Scheme = "http"
				if dohTestServer(hc, u.String()) {
					return u.String(), nil
				}
			}
		}
	}

	// TODO try checking the svcb record
	return "", fmt.Errorf("invalid doh server %s", server)
}
