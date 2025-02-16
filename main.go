package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

var DefaultStrategy = &LinearStrategy{}

type Jitaku struct {
	*ValidatedConfig

	httpClientOnce sync.Once
	httpClient     *http.Client
	logChan        chan *LogEntry
}

func NewJitaku(config *Config) (*Jitaku, error) {
	validated, err := config.ValidateConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to validate config: %w", err)
	}

	return NewJitakuValidated(validated), nil
}

func NewJitakuValidated(validated *ValidatedConfig) *Jitaku {
	return &Jitaku{
		logChan:         make(chan *LogEntry, 1),
		ValidatedConfig: validated,
	}
}

func (h *Jitaku) MakeWebUIHandler() http.Handler {
	mux := http.NewServeMux()
	h.RegisterWebUI(mux, h.logChan)

	return mux
}

type CacheItem struct {
	mu   sync.RWMutex
	cond *sync.Cond

	ips  []net.IP
	prev *int
	ttl  time.Time
}

func (item *CacheItem) Update(host string, strategy IForwardStrategy, upstreams []Upstream) error {
	if !item.mu.TryLock() {
		return nil
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
				return fmt.Errorf("failed to forward message: %w", err)
			}

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

	return nil
}

func (s *Jitaku) initHttpClient() {
	estrat := EForwardStrategyLinear
	if s.Strategy != nil {
		estrat = s.Strategy.Enum()
	}

	s.httpClient = createDoHClient(estrat.New(), func() []Upstream {
		return s.BootstrapUpstreams
	})
}

func (s *Jitaku) getHttpClient() *http.Client {
	s.httpClientOnce.Do(s.initHttpClient)
	return s.httpClient
}

type MessageResult struct {
	Response *dns.Msg
	Elapsed  time.Duration
	Upstream Upstream
}

func (r *MessageResult) Status() string {
	if r.Upstream != nil {
		switch u := r.Upstream.(type) {
		case *FilterUpstream:
			return "Blocked"

		case *RewriteUpstream:
			return "Rewritten"

		default:
			return fmt.Sprintf("Forwarded to %s", u.String())
		}
	}

	return "Error"
}

func (r *MessageResult) Detail() string {
	if r.Upstream == nil {
		return "No upstreams available"
	}

	switch u := r.Upstream.(type) {
	case *FilterUpstream:
		return u.Filter.Name()
	default:
	}

	return r.Elapsed.String()
}

func (c *Jitaku) ProcessMessage(r *dns.Msg) (*MessageResult, error) {
	start := time.Now()

	var res *dns.Msg
	var lastErr error
	var upstream Upstream
	for i := 0; i < len(c.Upstreams) && res == nil; {
		if c.Strategy == nil {
			c.Strategy = DefaultStrategy
		}

		if len(c.Upstreams[i]) < 1 {
			continue
		}

		msg, j, err := c.Strategy.ForwardMessage(c.Upstreams[i], r)
		log.Println(msg, j, err)
		upstream = c.Upstreams[i][j]
		if err != nil {
			lastErr = fmt.Errorf("Failed to forward message to upstream %s: %v", upstream, err)
		} else {
			res = msg
		}

		i++
	}

	if res == nil {
		// log.Println("No upstreams available")

		res = &dns.Msg{}
		res.SetRcode(r, dns.RcodeServerFailure)
		res.Extra = append(res.Extra, dns.TypeToRR[dns.TypeTXT]())
	}

	return &MessageResult{
		Response: res,
		Elapsed:  time.Now().Sub(start),
		Upstream: upstream,
	}, lastErr
}

func (c *Jitaku) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	// if len(r.Question) > 0 {
	// 	log.Printf("INFO: Received request with %d questions from %s", len(r.Question), w.RemoteAddr())
	// 	for _, q := range r.Question {
	// 		log.Printf("INFO:     %s %s", q.Name, dns.Type(q.Qtype))
	// 	}
	// }

	res, err := c.ProcessMessage(r)
	if err != nil {
		log.Printf("ERROR: Failed to process message: %v", err)
	}

	select {
	case c.logChan <- &LogEntry{
		Time:           time.Now(),
		Client:         w.RemoteAddr(),
		Request:        &r.Question[0],
		Response:       res.Status(),
		ResponseDetail: res.Detail(),
	}:
	default:
	}

	if err := w.WriteMsg(res.Response); err != nil {
		log.Printf("ERROR: Failed to write response: %v", err)
	}
}

type LogEntry struct {
	Time           time.Time
	Client         net.Addr
	Request        *dns.Question
	Response       string
	ResponseDetail string
}

func main() {
	host := flag.String("host", "0.0.0.0", "address to listen on (defaults to \"0.0.0.0\")")
	port := flag.Int("port", 53, "port to listen on")
	webui := flag.Bool("webui", false, "whether to run the web UI")
	webuiHost := flag.String("webui-host", "127.0.0.1", "address to listen on, empty for all (defaults to 127.0.0.1)")
	webuiPort := flag.Int("webui-port", 8808, "port to run the web UI on")
	flag.Parse()

	log.Println("INFO: validating config")

	start := time.Now()
	h, err := NewJitaku(defaultConfig)
	if err != nil {
		log.Fatalf("ERROR: Failed to create scrubbr: %v", err)
	}

	log.Printf("INFO: config validated in %s", time.Now().Sub(start))

	// TODO: Add support for multiple addresses
	addrs := []string{fmt.Sprintf("%s:%d", *host, *port)}

	var servers []*dns.Server
	for _, addr := range addrs {
		servers = append(servers, &dns.Server{Handler: h, Addr: addr, Net: "udp"})
		servers = append(servers, &dns.Server{Handler: h, Addr: addr, Net: "tcp"})
	}

	errChan := make(chan error)
	for _, s := range servers {
		go func() {
			errChan <- s.ListenAndServe()
		}()
	}

	for i := 0; i < len(servers); {
		s := servers[i]

		var addr net.Addr
		if s.Listener != nil {
			addr = s.Listener.Addr()
		} else if s.PacketConn != nil {
			addr = s.PacketConn.LocalAddr()
		}

		if addr == nil {
			select {
			case err := <-errChan:
				log.Fatalf("ERROR: Fatal error: %v", err)
			default:
				runtime.Gosched()
			}

			continue
		}

		log.Printf("INFO: DNS listening on %s://%s", addr.Network(), addr.String())

		i++
	}

	var webuiServer *http.Server
	if *webui {
		webuiServer = &http.Server{
			Handler: h.MakeWebUIHandler(),
		}

		l, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *webuiHost, *webuiPort))
		if err != nil {
			log.Printf("ERROR: Web UI failed to listen on %s:%d: %f", *webuiHost, *webuiPort, err)
			return
		}

		go func() {
			errChan <- webuiServer.Serve(l)
		}()

		log.Printf("INFO: Web UI listening on http://%s", l.Addr())
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		fmt.Println()
		log.Printf("INFO: Received (%v), shutting down", s)
	case err := <-errChan:
		log.Fatalf("ERROR: Fatal error: %v", err)
	}

	for _, s := range servers {
		if err := s.Shutdown(); err != nil {
			log.Fatalf("ERROR: Failed to shutdown server: %v", err)
		}
	}

	for range servers {
		select {
		case s := <-sig:
			log.Fatalf("ERROR: Received (%v), forcefully stopping", s)
		case err := <-errChan:
			if err != nil {
				log.Fatalf("ERROR: Fatal error while stopping: %v", err)
			}
		}
	}
}
