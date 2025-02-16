package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/miekg/dns"
)

type Upstream interface {
	String() string
	ForwardMessage(msg *dns.Msg) (*dns.Msg, error)
	Close() error
}

type RewriteUpstream struct {
	V4 map[string][]net.IP
	V6 map[string][]net.IP
}

func (c *RewriteUpstream) String() string {
	return "rewrite"
}

func (c *RewriteUpstream) Close() error {
	return nil
}

func (c *RewriteUpstream) ForwardMessage(msg *dns.Msg) (*dns.Msg, error) {
	if msg.Opcode != dns.OpcodeQuery || len(msg.Question) < 1 {
		return nil, nil
	}

	res := &dns.Msg{}
	res.SetReply(msg)
	for _, q := range msg.Question {
		if q.Qclass != dns.ClassINET {
			continue
		}

		if q.Qtype == dns.TypeA || q.Qtype == dns.TypeANY {
			ips, ok := c.V4[q.Name]
			if !ok && strings.HasSuffix(q.Name, ".") {
				ips, ok = c.V4[q.Name[:len(q.Name)-1]]
			}

			for _, ip := range ips {
				res.Answer = append(res.Answer, &dns.A{
					Hdr: dns.RR_Header{
						Name:   q.Name,
						Rrtype: dns.TypeA,
						Class:  dns.ClassINET,
						Ttl:    300,
					},
					A: ip,
				})
			}
		}

		if q.Qtype == dns.TypeAAAA || q.Qtype == dns.TypeANY {
			ips, ok := c.V6[q.Name]
			if !ok && strings.HasSuffix(q.Name, ".") {
				ips, ok = c.V6[q.Name[:len(q.Name)-1]]
			}

			for _, ip := range ips {
				res.Answer = append(res.Answer, &dns.AAAA{
					Hdr: dns.RR_Header{
						Name:   q.Name,
						Rrtype: dns.TypeAAAA,
						Class:  dns.ClassINET,
						Ttl:    300,
					},
					AAAA: ip,
				})
			}
		}
	}

	if len(res.Answer) == 0 {
		return nil, nil
	}

	return res, nil
}

type ConnPoolUpstream struct {
	Net      string
	Addr     string
	MaxConns int

	mu     sync.Mutex
	cond   sync.Cond
	closed bool
	total  int
	conns  []*dns.Conn
}

func (c *ConnPoolUpstream) String() string {
	net := c.Net
	if net == "" {
		net = "udp"
	}

	return fmt.Sprintf("%s://%s", net, c.Addr)
}

func (c *ConnPoolUpstream) getConn() (*dns.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("closed")
	}

	if c.cond.L == nil {
		c.cond.L = &c.mu
	}

	if c.Net == "" {
		c.Net = "udp"
	}

	if len(c.conns) == 0 {
		if c.MaxConns == 0 || c.total < c.MaxConns {
			conn, err := dns.Dial(c.Net, c.Addr)
			if err != nil {
				return nil, fmt.Errorf("Failed to dial addr %s: %w", c.Addr, err)
			}

			c.total += 1
			return conn, nil
		} else {
			for {
				c.cond.Wait()
			}
		}
	}

	conn := c.conns[len(c.conns)-1]
	c.conns = c.conns[:len(c.conns)-1]
	return conn, nil
}

func (c *ConnPoolUpstream) returnConn(conn *dns.Conn) {
	defer c.cond.Signal()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.conns = append(c.conns, conn)
}

func (c *ConnPoolUpstream) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cond.L == nil {
		c.cond.L = &c.mu
	}

	for c.total > 0 {
		if len(c.conns) > 0 {
			conn := c.conns[len(c.conns)-1]
			if err := conn.Close(); err != nil {
				return err
			}

			c.conns = c.conns[:len(c.conns)-1]
			c.total -= 1
		} else {
			c.cond.Wait()
		}
	}

	return nil
}

func (c *ConnPoolUpstream) ForwardMessage(msg *dns.Msg) (*dns.Msg, error) {
	conn, err := c.getConn()
	if err != nil {
		return nil, fmt.Errorf("Failed to get conn: %w", err)
	}

	defer c.returnConn(conn)

	if err := conn.WriteMsg(msg); err != nil {
		return nil, fmt.Errorf("Failed to write upstream request: %w", err)
	}

	res, err := conn.ReadMsg()
	if err != nil {
		return nil, fmt.Errorf("Failed to read upstream response: %w", err)
	}

	return res, nil
}

type DoHUpstream struct {
	Client *http.Client
	URL    string
}

func (d *DoHUpstream) String() string {
	return d.URL
}

func (d *DoHUpstream) ForwardMessage(msg *dns.Msg) (*dns.Msg, error) {
	client := http.DefaultClient
	if d.Client != nil {
		client = d.Client
	}

	return dohExchange(client, d.URL, msg)
}

func (d *DoHUpstream) Close() error {
	return nil
}
