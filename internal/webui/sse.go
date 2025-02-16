package webui

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"syscall"
	"time"
)

type SSEMessage struct {
	Event string
	Data  string
}

func StartSSE(w http.ResponseWriter, msgChan <-chan SSEMessage) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	w.WriteHeader(200)

	t := time.NewTimer(0)
	for {
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}

		t.Reset(30 * time.Second)

		var msg SSEMessage
		select {
		case m, ok := <-msgChan:
			if !ok {
				break
			}

			msg = m
			// data = strings.ReplaceAll(data, "\n", "")
		case <-t.C:
			msg.Event = "ping"
			msg.Data = ""
		}

		msg.Data = strings.TrimSpace(msg.Data)

		if _, err := w.Write([]byte("event: " + msg.Event + "\n" + "data: " + strings.Join(strings.Split(msg.Data, "\n"), "\ndata: ") + "\n\n")); err != nil {
			if !errors.Is(err, syscall.EPIPE) {
				log.Printf("WARN: Web UI logs event stream write failed: %v", err)
			}

			break
		}

		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}
