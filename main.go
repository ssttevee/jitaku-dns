package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
	"github.com/ssttevee/jitaku-dns/internal"
	"github.com/ssttevee/jitaku-dns/internal/config"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream"
	"github.com/ssttevee/jitaku-dns/internal/webui"
)

var (
	enableWebui = flag.Bool("webui", false, "whether to run the web UI")
	webuiHost   = flag.String("webui-host", "127.0.0.1", "address to listen on (defaults to 127.0.0.1)")
	webuiPort   = flag.Int("webui-port", 8808, "port to run the web UI on")
)

type Jitaku struct {
	*config.InitializedConfig

	configPath string

	logChan chan *internal.LogEntry

	updateFilterTimer *time.Timer

	appctx context.Context
	close  context.CancelFunc

	cache4 sync.Map
	cache6 sync.Map
}

func writeConfig(cfg *config.Config, configPath string) error {
	if err := os.MkdirAll(path.Dir(configPath), 0700); err != nil {
		return err
	}

	if err := os.WriteFile(configPath, cfg.Serialize(), 0600); err != nil {
		return err
	}

	return nil
}

func NewJitakuFromConfigPath(configPath string) (*Jitaku, error) {
	var cfg *config.Config
	if configPath == "" {
		cfg = config.DefaultConfig
	} else if data, err := os.ReadFile(configPath); errors.Is(err, os.ErrNotExist) {
		log.Println("INFO: config file not found")
		cfg = config.DefaultConfig
		if err := writeConfig(cfg, configPath); err != nil && !errors.Is(err, os.ErrPermission) {
			log.Printf("WARN: failed to write default config to disk: %v", err)
		} else if err == nil {
			log.Println("INFO: wrote default config to disk")
		}
	} else {
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		flag.Parse()

		for {
			if cfg, err = config.Parse(data); err != nil {
				if *enableWebui {
					// if webui is enabled, serve a safe-mode page instead of crashing
					data, err = webui.UpdatedConfigFromSafemode(fmt.Sprintf("%s:%d", *webuiHost, *webuiPort), err.Error(), data)
					if err != nil {
						return nil, fmt.Errorf("config safe mode: %w", err)
					}

					continue
				} else {
					return nil, fmt.Errorf("failed to parse config: %w", err)
				}
			}

			break
		}
	}

	os.Stderr.WriteString(string(cfg.Serialize()))

	j := NewJitaku(cfg)

	j.configPath = configPath

	return j, nil
}

func NewJitaku(config *config.Config) *Jitaku {
	ctx, cancel := context.WithCancel(context.Background())
	j := &Jitaku{
		logChan:           make(chan *internal.LogEntry, 1),
		InitializedConfig: config.Initialize(),
		appctx:            ctx,
		close:             cancel,
	}

	j.startFilterUpdater(ctx)

	return j
}

func (j *Jitaku) startFilterUpdater(ctx context.Context) {
	if j.updateFilterTimer == nil && j.InitializedConfig.FilterUpdateIntervalDuration() > 0 {
		go func() {
			defer func() {
				j.updateFilterTimer = nil
			}()

			j.updateFilterTimer = time.NewTimer(j.InitializedConfig.FilterUpdateIntervalDuration())
			for {
				j.updateFilterTimer.Reset(j.InitializedConfig.FilterUpdateIntervalDuration())

				select {
				case <-ctx.Done():
					return
				case <-j.updateFilterTimer.C:
					start := time.Now()
					log.Println("INFO: Updating filters")
					for _, upstream := range j.InitializedConfig.Filters {
						if err := upstream.(*config.LazyUpstream).Reinit(ctx); err != nil {
							log.Printf("WARN: Failed to update filter %q: %v", upstream.String(), err)
						}
					}

					log.Printf("INFO: Updated filters in %v", time.Since(start))
				}
			}
		}()
	} else if j.updateFilterTimer != nil {
		if !j.updateFilterTimer.Stop() {
			select {
			case <-j.updateFilterTimer.C:
			}
		}

		j.updateFilterTimer.Reset(j.InitializedConfig.FilterUpdateIntervalDuration())
	}
}

func (j *Jitaku) LogChan() <-chan *internal.LogEntry {
	return j.logChan
}

func (h *Jitaku) GetConfig() *config.Config {
	return h.InitializedConfig.Config()
}

func (h *Jitaku) GetConfigPath() string {
	return h.configPath
}

func (h *Jitaku) SetConfig(ctx context.Context, c *config.Config) error {
	initialized := c.Initialize()
	if err := initialized.Validate(ctx); err != nil {
		return err
	}

	if h.configPath != "" {
		if err := writeConfig(c, h.configPath); err != nil {
			if !errors.Is(err, os.ErrPermission) {
				return fmt.Errorf("failed to write config file: %w", err)
			}

			log.Printf("WARN: failed to write config to disk: %v", err)
		} else {
			log.Println("INFO: wrote config to disk")
		}
	}

	h.InitializedConfig = initialized

	h.startFilterUpdater(h.appctx)

	return nil
}

type cacheEntry struct {
	ans []dns.RR
	exp time.Time
	ups upstream.Upstream
}

func (c *Jitaku) cacheForType(t dns.Type) *sync.Map {
	switch t {
	case dns.Type(dns.TypeA):
		return &c.cache4
	case dns.Type(dns.TypeAAAA):
		return &c.cache6
	default:
		return nil
	}
}

func (c *Jitaku) getCachedResponse(r *dns.Msg) (*internal.MessageResult, bool) {
	start := time.Now()
	if r.Opcode != dns.OpcodeQuery || len(r.Question) < 1 {
		return nil, false
	}

	res := &dns.Msg{}
	res.SetReply(r)
	var ups upstream.Upstream
	for _, q := range r.Question {
		if q.Qtype != dns.TypeA && q.Qtype != dns.TypeAAAA {
			// only cache A and AAAA queries
			return nil, false
		}

		name := dns.Fqdn(q.Name)
		name = strings.TrimSuffix(name, ".")

		cache := c.cacheForType(dns.Type(q.Qtype))
		if cache == nil {
			return nil, false
		}

		value, ok := cache.Load(name)
		if !ok {
			return nil, false
		}

		entry, ok := value.(*cacheEntry)
		if !ok {
			return nil, false
		}

		if entry.exp.Before(time.Now()) {
			// expired
			cache.Delete(name)
			return nil, false
		}

		res.Answer = append(res.Answer, entry.ans...)
		ups = entry.ups
	}

	return &internal.MessageResult{
		Response: res,
		Cached:   true,
		Elapsed:  time.Since(start),
		Upstream: ups,
	}, true
}

func (c *Jitaku) cacheResponse(res *internal.MessageResult) {
	if res.Response.Opcode != dns.OpcodeQuery || len(res.Response.Question) != 1 || res.Cached {
		return
	}

	name := dns.Fqdn(res.Response.Question[0].Name)
	name = strings.TrimSuffix(name, ".")

	var ans4, ans6 []dns.RR
	var ttl4, ttl6 uint32 = math.MaxUint32, math.MaxUint32
	for _, rr := range res.Response.Answer {
		switch rr := rr.(type) {
		case *dns.A:
			ans4 = append(ans4, rr)
			if rr.Hdr.Ttl < ttl4 {
				ttl4 = rr.Hdr.Ttl
			}
		case *dns.AAAA:
			ans6 = append(ans6, rr)
			if rr.Hdr.Ttl < ttl6 {
				ttl6 = rr.Hdr.Ttl
			}
		}
	}

	if len(ans4) > 0 && ttl4 < math.MaxUint32 {
		c.cache4.Store(name, &cacheEntry{
			ans: ans4,
			exp: time.Now().Add(time.Duration(ttl4) * time.Second),
			ups: res.Upstream,
		})
	}

	if len(ans6) > 0 && ttl6 < math.MaxUint32 {
		c.cache6.Store(name, &cacheEntry{
			ans: ans6,
			exp: time.Now().Add(time.Duration(ttl6) * time.Second),
			ups: res.Upstream,
		})
	}
}

func (c *Jitaku) processMessage(ctx context.Context, r *dns.Msg) (*internal.MessageResult, error) {
	res, ok := c.getCachedResponse(r)
	if ok {
		return res, nil
	}

	res, err := c.ProcessMessage(ctx, r)
	if err != nil {
		return nil, err
	}

	if res.Upstream.IsReal() {
		c.cacheResponse(res)
	}

	return res, nil
}

func (c *Jitaku) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	res, err := c.processMessage(context.Background(), r)
	if err != nil {
		log.Printf("ERROR: Failed to process message: %v", err)
	}

	select {
	case c.logChan <- &internal.LogEntry{
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

func getConfigPath() string {
	if path := os.Getenv("JITAKU_CONFIG"); path != "" {
		return path
	}

	// basic arg parser to read config path
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-config" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}

		if strings.HasPrefix(os.Args[i], "-config=") && len(os.Args[i]) > 8 {
			return os.Args[i][8:]
		}
	}

	return config.DefaultConfigPath
}

func main() {
	host := flag.String("host", "0.0.0.0", "address to listen on (defaults to \"0.0.0.0\")")
	port := flag.Int("port", 53, "port to listen on")

	configPath := getConfigPath()
	if configPath == "" {
		log.Println("INFO: loading default config")
	} else {
		log.Println("INFO: loading config from", configPath)
	}

	h, err := NewJitakuFromConfigPath(configPath)
	if err != nil {
		log.Fatalf("ERROR: Failed to create server: %v", err)
	}

	defer h.close()

	go func() {
		// init config on in the background
		if err := h.Validate(context.Background()); err != nil {
			log.Printf("WARN: Failed to validate config: %v", err)
		}
	}()

	flag.String("config", configPath, "config file path")
	flag.Parse()

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
	if *enableWebui {
		webuiServer = &http.Server{
			Handler: webui.MakeHandler(h),
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
