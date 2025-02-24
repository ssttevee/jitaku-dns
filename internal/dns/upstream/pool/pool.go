package pool

import (
	"context"
	"fmt"
	"sync"

	"github.com/miekg/dns"
)

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

func (c *ConnPoolUpstream) IsReal() bool {
	return true
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

func (c *ConnPoolUpstream) ForwardMessage(ctx context.Context, msg *dns.Msg) (*dns.Msg, error) {
	conn, err := c.getConn()
	if err != nil {
		return nil, fmt.Errorf("failed to get conn: %w", err)
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
