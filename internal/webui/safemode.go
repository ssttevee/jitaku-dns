package webui

import (
	"errors"
	"log"
	"net"
	"net/http"
)

func UpdatedConfigFromSafemode(address string, msg string, data []byte) ([]byte, error) {
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}

	defer l.Close()

	var s http.Server

	mux := http.NewServeMux()

	var updated bool

	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if yaml := r.Form.Get("yaml"); yaml != "" {
			data = []byte(yaml)
			updated = true
		}

		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusSeeOther)
	})

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if updated {
			w.Write([]byte("config received, refresh this page to see if it worked"))
			go s.Close()
			return
		}

		w.Write([]byte(RootLayout(
			RootLayoutProps{
				req:   r,
				title: "Settings",
				stylesheets: []string{
					"https://cdn.jsdelivr.net/npm/prismjs@1.29.0/themes/prism.min.css",
					"https://cdn.jsdelivr.net/npm/prismjs@1.29.0/plugins/line-numbers/prism-line-numbers.min.css",
					"https://cdn.jsdelivr.net/gh/WebCoder49/code-input@2.4.0/code-input.min.css",
					"https://cdn.jsdelivr.net/gh/WebCoder49/code-input@2.4.0/plugins/prism-line-numbers.min.css",
				},
				scripts: []string{
					"https://cdn.jsdelivr.net/npm/prismjs@1.29.0/prism.min.js",
					"https://cdn.jsdelivr.net/npm/prismjs@1.29.0/components/prism-yaml.min.js",
					"https://cdn.jsdelivr.net/npm/prismjs@1.29.0/plugins/line-numbers/prism-line-numbers.min.js",
					"https://cdn.jsdelivr.net/gh/WebCoder49/code-input@2.4.0/code-input.min.js",
					"https://cdn.jsdelivr.net/gh/WebCoder49/code-input@2.4.0/plugins/indent.min.js",
				},
				scriptSnippets: []string{
					`codeInput.registerTemplate("syntax-highlighted", codeInput.templates.prism(Prism, []));`,
				},
				navItems: []navItem{},
			},
			`<form method="POST" class="d-flex flex-column flex-grow-1 gap-4 pb-4">
<div class="d-flex justify-content-between">
	<div><h6>Safe Mode</h6><p class="my-0 small text-secondary">An error occurred while parsing the config.</p></div>
	<div><button class="btn btn-primary">Save</button></div>
</div>
<pre class="m-0">`+msg+`</pre>
<code-input class="flex-grow-1" language="yaml" placeholder="" name="yaml">`+string(data)+`</code-input>
</form>`,
		)))
	})

	mux.Handle("GET /assets/", assetsHandler)

	s.Handler = mux

	errChan := make(chan error)
	go func() {
		errChan <- s.Serve(l)
	}()

	log.Printf("INFO: Safe mode web interface started")
	log.Printf("INFO: Please navigate to http://%s to correct the config", l.Addr())

	if err := <-errChan; err != nil {
		if !errors.Is(err, http.ErrServerClosed) {
			return nil, err
		}
	}

	return data, nil
}
