package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/miekg/dns"
	"github.com/ssttevee/jitaku-dns/internal"
	"github.com/ssttevee/jitaku-dns/internal/config"
	"github.com/ssttevee/jitaku-dns/internal/webui"
)

type Jitaku struct {
	*config.InitializedConfig

	logChan chan *internal.LogEntry
}

func NewJitakuFromConfigPath(configPath string) (*Jitaku, error) {
	var cfg *config.Config
	if configPath == "" {
		cfg = config.DefaultConfig
	} else if data, err := os.ReadFile(configPath); errors.Is(err, os.ErrNotExist) {
		log.Println("INFO: config file not found")
		log.Println("INFO: writing default config")
		cfg = config.DefaultConfig
		if err := os.MkdirAll(configPath, 0700); err == nil {
			if err := os.WriteFile(configPath, config.DefaultConfig.Serialize(), 0600); err != nil && errors.Is(err, os.ErrPermission) {
				log.Printf("WARN: Failed to write default config file to disk: %v", err)
			}
		}
	} else {
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		if cfg, err = config.Parse(data); err != nil {
			return nil, fmt.Errorf("failed to parse config: %w", err)
		}
	}

	fmt.Println(string(cfg.Serialize()))

	return NewJitaku(cfg), nil
}

func NewJitaku(config *config.Config) *Jitaku {
	return &Jitaku{
		logChan:           make(chan *internal.LogEntry, 1),
		InitializedConfig: config.Initialize(),
	}
}

func (j *Jitaku) LogChan() <-chan *internal.LogEntry {
	return j.logChan
}

func (h *Jitaku) MakeWebUIHandler() http.Handler {
	mux := http.NewServeMux()
	webui.RegisterWebUI(mux, h)

	return mux
}

func (h *Jitaku) GetConfig() *config.Config {
	return h.InitializedConfig.Config()
}

func (h *Jitaku) SetConfig(c *config.Config) error {
	initialized := c.Initialize()
	if err := initialized.Validate(); err != nil {
		return err
	}

	h.InitializedConfig = initialized

	return nil
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

func main() {
	host := flag.String("host", "0.0.0.0", "address to listen on (defaults to \"0.0.0.0\")")
	port := flag.Int("port", 53, "port to listen on")
	webui := flag.Bool("webui", false, "whether to run the web UI")
	webuiHost := flag.String("webui-host", "127.0.0.1", "address to listen on (defaults to 127.0.0.1)")
	webuiPort := flag.Int("webui-port", 8808, "port to run the web UI on")
	configPath := flag.String("config", config.DefaultConfigPath, "config file path")
	flag.Parse()

	if *configPath == "" {
		log.Println("INFO: loading default config")
	} else {
		log.Println("INFO: loading config from", *configPath)
	}

	h, err := NewJitakuFromConfigPath(*configPath)
	if err != nil {
		log.Fatalf("ERROR: Failed to create scrubbr: %v", err)
	}

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
