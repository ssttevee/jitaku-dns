package webui

import (
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/miekg/dns"
	"github.com/ssttevee/jitaku-dns/internal"
	"github.com/ssttevee/jitaku-dns/internal/config"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream"
)

var digTypes = []dns.Type{
	dns.Type(dns.TypeA),
	dns.Type(dns.TypeAAAA),
	dns.Type(dns.TypeANY),
	dns.Type(dns.TypeCAA),
	dns.Type(dns.TypeCNAME),
	dns.Type(dns.TypeDNSKEY),
	dns.Type(dns.TypeDS),
	dns.Type(dns.TypeMX),
	dns.Type(dns.TypeNS),
	dns.Type(dns.TypePTR),
	dns.Type(dns.TypeSOA),
	dns.Type(dns.TypeSRV),
	dns.Type(dns.TypeSVCB),
	dns.Type(dns.TypeTLSA),
	dns.Type(dns.TypeTSIG),
	dns.Type(dns.TypeTXT),
}

type Controller interface {
	LogChan() <-chan *internal.LogEntry
	ProcessMessage(msg *dns.Msg) (*internal.MessageResult, error)
	GetConfig() *config.Config
	SetConfig(*config.Config) error
	GetConfigPath() string
}

func Max(x, y int) int {
	if x > y {
		return x
	}

	return y
}

func RegisterWebUI(mux *http.ServeMux, c Controller) {
	logsQueue := newPubSub(c.LogChan())

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(RootLayout(RootLayoutProps{
			pathname: r.URL.Path,
		}, "<h1>Dashboard</h1>")))
	})

	mux.HandleFunc("GET /dig", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		typ := dns.Type(dns.TypeA)
		if t, err := strconv.ParseInt(q.Get("type"), 10, 16); err == nil {
			typ = dns.Type(t)
		}

		name := q.Get("name")

		var res *internal.MessageResult
		var err error
		if name != "" && typ != 0 {
			msg := &dns.Msg{}
			msg.SetQuestion(dns.Fqdn(name), uint16(typ))

			res, err = c.ProcessMessage(msg)
		}

		resultContent := DigResult(DigResultProps{
			result: res,
			err:    err,
		})

		var typeOptions string
		for _, t := range digTypes {
			var attrs string
			if typ == t {
				attrs = ` checked`
			}

			typeOptions += `
<div class="form-check form-check-inline">
<input class="form-check-input" type="radio" name="type" id="type-` + t.String() + `" value="` + strconv.Itoa(int(t)) + `"` + attrs + `>
	<label class="form-check-label" for="type-` + t.String() + `">` + t.String() + `</label>
</div>
`
		}

		if r.Header.Get("Hx-Request") == "true" {
			w.Write([]byte(resultContent))
			return
		}

		w.Write([]byte(RootLayout(
			RootLayoutProps{
				pathname: r.URL.Path,
			},
			`
<h1>Dig</h1>
<p class="text-secondary">Queries made on this page are omitted from the logs.</p>
<div class="card mt-5">
<form class="card-body" hx-get="/dig" hx-trigger="keyup changed delay:500ms target:.form-control, change target:input" hx-target="#dig-result" hx-push-url="true">
	<div class="mb-3">
		<label for="name" class="form-label">Name</label>
		<input type="text" class="form-control" id="name" name="name" placeholder="example.com" value="`+html.EscapeString(name)+`">
	</div>
	<div>
		<label class="form-label">Type</label>
		<div class="card">
			<div class="card-body">`+typeOptions+`</div>
		</div>
	</div>
</form>
</div>

<div class="row mt-5">
<div class="col" id="dig-result">
`+resultContent+`
</div>
</div>
`,
		)))
	})

	mux.HandleFunc("GET /logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			w.Write([]byte(RootLayout(
				RootLayoutProps{
					title:    "Logs",
					pathname: r.URL.Path,
				},
				`
<h1>Logs</h1>
<p class="text-secondary">No logs are stored. Only requests made after this page was opened are shown here.</p>
<table class="table mt-5">
	<thead>
		<tr>
			<th scope="col">Time</th>
			<th scope="col">Request</th>
			<th scope="col">Response</th>
			<th scope="col">Client</th>
		</tr>
	</thead>
	<tbody hx-ext="sse" sse-connect="/logs" sse-swap="message" hx-swap="afterbegin"></tbody>
</table>
`,
			)))
			return
		}

		msgChan := make(chan sseMessage, 1)
		go func() {
			for entry := range logsQueue.Subscribe() {
				msgChan <- sseMessage{
					Event: "message",
					Data: LogRow(LogRowProps{
						entry: entry,
					}),
				}
			}
		}()

		startSSE(w, msgChan)
	})

	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		cfg := c.GetConfig()

		var yaml string
		if r.URL.Query().Get("yaml") == "1" {
			yaml = string(cfg.Serialize())
		}

		w.Write([]byte(RootLayout(
			RootLayoutProps{
				title:    "Settings",
				pathname: r.URL.Path,
				stylesheets: []string{
					"https://cdn.jsdelivr.net/npm/prismjs@1.29.0/themes/prism.min.css",
					"https://cdn.jsdelivr.net/npm/prismjs@1.29.0/plugins/line-numbers/prism-line-numbers.min.css",
					"https://cdn.jsdelivr.net/gh/WebCoder49/code-input@2.4.0/code-input.min.css",
					"https://cdn.jsdelivr.net/gh/WebCoder49/code-input@2.4.0/plugins/prism-line-numbers.min.css",
				},
				scripts: []string{
					"https://cdn.jsdelivr.net/npm/prismjs@1.29.0/prism.min.js",
					"https://cdn.jsdelivr.net/npm/prismjs@1.29.0/components/prism-ignore.min.js",
					"https://cdn.jsdelivr.net/npm/prismjs@1.29.0/components/prism-yaml.min.js",
					"https://cdn.jsdelivr.net/npm/prismjs@1.29.0/plugins/line-numbers/prism-line-numbers.min.js",
					"https://cdn.jsdelivr.net/gh/WebCoder49/code-input@2.4.0/code-input.min.js",
					"https://cdn.jsdelivr.net/gh/WebCoder49/code-input@2.4.0/plugins/indent.min.js",
					"https://cdn.jsdelivr.net/gh/WebCoder49/code-input@2.4.0/plugins/indent.min.js",
				},
				scriptSnippets: []string{
					`codeInput.registerTemplate("syntax-highlighted", codeInput.templates.prism(Prism, []));`,
				},
			},
			SettingsContent(SettingsContentProps{
				configpath: c.GetConfigPath(),
				yaml:       yaml,
				cfg:        cfg,
			}),
		)))
	})

	mux.HandleFunc("POST /settings", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		q, err := url.ParseQuery(string(body))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if r.Header.Get("Hx-Request") != "true" {
			path := r.URL.Path
			if r.URL.RawQuery != "" {
				path += "?" + r.URL.RawQuery
			}

			http.Redirect(w, r, path, http.StatusSeeOther)
			return
		}

		yamlmode := r.URL.Query().Get("yaml") == "1"
		var yaml string
		var cfg *config.Config
		var cfgerr error
		if yamlmode {
			yaml = q.Get("yaml")
			if yaml == "" {
				path := r.URL.Path
				if r.URL.RawQuery != "" {
					path += "?" + r.URL.RawQuery
				}

				http.Redirect(w, r, path, http.StatusSeeOther)
				return
			}

			config, err := config.Parse([]byte(yaml))
			if err != nil {
				cfgerr = err
			} else {
				cfg = config
			}
		} else {
			cfg = &config.Config{}
			if bootstraps := q.Get("bootstraps"); bootstraps != "" {
				cfg.Upstream.Bootstrap = strings.Split(bootstraps, "\n")
			}

			if servers := q.Get("servers"); servers != "" {
				cfg.Upstream.Servers = strings.Split(servers, "\n")
			}

			if fallbacks := q.Get("fallbacks"); fallbacks != "" {
				cfg.Upstream.Fallback = strings.Split(fallbacks, "\n")
			}

			if filters := q.Get("filters"); filters != "" {
				cfg.Filters = strings.Split(filters, "\n")
			}

			if strategy := upstream.ForwardStrategyKind(q.Get("strategy")); strategy.Valid() {
				cfg.Upstream.Strategy = &strategy
			}
		}

		if cfg != nil && cfgerr == nil {
			cfgerr = c.SetConfig(cfg)
		}

		var msg string
		if cfgerr != nil {
			msg = cfgerr.Error()
		} else {
			msg = "Config saved successfully."
		}

		w.Write([]byte(SettingsContent(SettingsContentProps{
			configpath: c.GetConfigPath(),
			yaml:       yaml,
			cfg:        cfg,
			msg:        msg,
		})))
	})

	registerGoKrazyRoutes(mux)
}

type SettingsContentProps struct {
	configpath string
	yaml       string

	cfg *config.Config
	msg string
}

func SettingsContent(props SettingsContentProps) string {
	var errorMessage string
	if props.msg != "" {
		errorMessage = `
<div class="row gy-4">
<pre>` + props.msg + `</pre>
</div>`
	}

	var body string
	if props.yaml != "" {
		body = `
<code-input class="flex-grow-1" language="yaml" placeholder="" name="yaml">` + props.yaml + `</code-input>
`
	} else if props.cfg != nil {

		currentStrategy := upstream.DefaultStrategy.Enum()
		if s := props.cfg.Upstream.Strategy; s != nil {
			currentStrategy = *s
		}

		var radioOptions string
		for _, strategy := range upstream.ForwardStrategyKinds {
			var attrs string
			if currentStrategy == strategy {
				attrs = ` checked`
			}

			radioOptions += `
	<div class="col form-check">
			<input class="form-check-input" type="radio" name="strategy" id="strategy-` + string(strategy) + `" value="` + string(strategy) + `"` + attrs + `>
			<label class="form-check-label text-nowrap" for="strategy-` + string(strategy) + `">` + string(strategy.String()) + `</label>
	</div>
	`
		}

		body = `
<div class="row">
<div class="col col-lg-6">
	<div class="card">
		<div class="card-body">
			<div class="mb-3">
				<label for="servers-text-area" class="form-label">
					<h6>Upstream Servers</h6>
					<p class="my-0 small text-secondary">Enter one server address per line. (Lines starting with <code>#</code> are ignored)</p>
				</label>
				<code-input language="ignore" placeholder="" id="servers-text-area" name="servers">` + strings.Join(props.cfg.Upstream.Servers, "\n") + `</code-input>
			</div>
			<fieldset class="mb-3">
				<legend><h6>Strategy</h6></legend>
				<p class="mt-0 small text-secondary">How to select upstream server. (Lines starting with <code>#</code> are ignored)</p>
				<div class="container">
					<div class="row">` + radioOptions + `</div>
				</div>
			</fieldset>
			<div class="mb-3">
				<label for="bootstrap-text-area" class="form-label">
					<h6>Bootstrap Servers</h6>
					<p class="my-0 small text-secondary">Enter one server address per line. (Lines starting with <code>#</code> are ignored)</p>
				</label>
				<code-input language="ignore" placeholder="" id="bootstrap-text-area" name="bootstraps">` + strings.Join(props.cfg.Upstream.Bootstrap, "\n") + `</code-input>
			</div>
			<div>
				<label for="fallback-text-area" class="form-label">
					<h6>Fallback Servers</h6>
					<p class="my-0 small text-secondary">Enter one server address per line. (Lines starting with <code>#</code> are ignored)</p>
				</label>
				<code-input language="ignore" placeholder="" id="fallback-text-area" name="fallbacks">` + strings.Join(props.cfg.Upstream.Fallback, "\n") + `</code-input>
			</div>
		</div>
	</div>
</div>
<div class="col col-lg-6">
	<div class="card mb-4">
		<div class="card-body">
			<label for="filters-text-area" class="form-label">
				<h6>Filters</h6>
				<p class="my-0 small text-secondary">Enter one url per line. ABP and hosts file links are supported.</p>
			</label>
			<code-input language="ignore" placeholder="" id="filters-text-area" name="filters">` + strings.Join(props.cfg.Filters, "\n") + `</code-input>
		</div>
	</div>
	<div class="card">
		<div class="card-body">
			<label for="rewrites-text-area" class="form-label">
				<h6>Rewrites</h6>
				<p class="my-0 small text-secondary">Enter in the format of <code>/etc/hosts</code>. (Lines starting with <code>#</code> are ignored)</p>
			</label>
			<code-input language="ignore" placeholder="" id="rewrites-text-area" name="rewrites">` + strings.Join(props.cfg.Rewrites, "\n") + `</code-input>
		</div>
	</div>
</div>
</div>`
	} else {
		body = `config is nil (this should not happen)`
	}

	var yamlbtn string
	var url string
	if props.yaml != "" {
		url = "/settings?yaml=1"
		yamlbtn = `<a href="/settings" class="btn btn-secondary">Config Mode</a>`
	} else {
		url = "/settings"
		yamlbtn = `<a href="/settings?yaml=1" class="btn btn-secondary">YAML Mode</a>`
	}

	var headersubtext string
	if props.configpath == "" {
		headersubtext = "Config path not set. Changes will not persist after restart."
	} else {
		headersubtext = "Changes will be written to disk if server has write permissions. (Config path: <code>" + props.configpath + "</code>)"
	}

	return `
<form hx-post="` + url + `" hx-swap="outerHTML" class="flex-grow-1 d-flex flex-column" hx-on--after-settle="` + html.EscapeString(`document.querySelectorAll("code-input").forEach((elem) => {elem.connectedCallback();elem.attributeChangedCallback("template", "syntax-highlighted", "syntax-highlighted")})`) + `">
<div class="d-flex justify-content-between">
	<h1>Settings</h1>
	<div>
		` + yamlbtn + `
		<button class="btn btn-primary">Save</button>
	</div>
</div>
<p class="text-secondary">` + headersubtext + `</p>
<div class="my-5 line-numbers flex-grow-1 d-flex flex-column">
` + errorMessage + body + `
</div>
</form>
`
}

type DigResultProps struct {
	result *internal.MessageResult
	err    error
}

func DigResult(props DigResultProps) string {
	if props.result != nil {
		return `
<div class="card">
	<div class="card-body">
		<pre class="mb-0">` + strings.TrimSpace(html.EscapeString(props.result.Response.String())) + `</pre>
	</div>
</div>
`
	}

	if props.err != nil {
		return `
<div class="card">
	<div class="card-body">
		<pre>` + html.EscapeString(props.err.Error()) + `</pre>
	</div>
</div>
`
	}

	return ""
}

type LogRowProps struct {
	entry *internal.LogEntry
}

func LogRow(props LogRowProps) string {
	return `
    <tr>
      <td scope="col" class="align-middle"><p class="text-nowrap my-0">` + props.entry.Time.Format("15:04:05") + `</p><p class="text-nowrap small my-0">` + props.entry.Time.Format("2006/01/02") + `</p></td>
      <td scope="col" class="align-middle"><p class="text-nowrap my-0 font-monospace">` + html.EscapeString(props.entry.Request.String()) + `</p></td>
      <td scope="col" class="align-middle"><p class="text-nowrap my-0">` + html.EscapeString(props.entry.Response) + `</p><p class="text-nowrap small my-0">` + html.EscapeString(props.entry.ResponseDetail) + `</p></td>
      <td scope="col" class="align-middle"><p class="text-nowrap my-0">` + props.entry.Client.String() + `</p></td>
    </tr>
`
}

type RootLayoutProps struct {
	title          string
	pathname       string
	darkmode       bool
	stylesheets    []string
	scripts        []string
	scriptSnippets []string
}

type navItem struct {
	Name string
	Path string
}

func RootLayout(props RootLayoutProps, children ...string) string {
	titlePrefix := props.title
	if titlePrefix != "" {
		titlePrefix += " - "
	}

	var rootAttrs string
	if props.darkmode {
		rootAttrs = ` data-bs-theme="dark"`
	}

	navItems := append([]navItem{
		{
			Name: "Dashboard",
			Path: "/",
		},
		{
			Name: "Dig",
			Path: "/dig",
		},
		{
			Name: "Logs",
			Path: "/logs",
		},
		{
			Name: "Settings",
			Path: "/settings",
		},
	}, gokrazyNavItems...)

	var navItemsHtml string
	for _, item := range navItems {
		var extraClasses string
		var extraAttrs string
		if item.Path == props.pathname {
			extraClasses = " active"
			extraAttrs = ` aria-current="page"`
		}

		navItemsHtml += `<li class="nav-item"><a class="nav-link` + extraClasses + `"` + extraAttrs + ` href="` + item.Path + `">` + item.Name + `</a></li>`
	}

	var stylesheets string
	for _, href := range append([]string{
		"https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/css/bootstrap.min.css",
	}, props.stylesheets...) {
		stylesheets += `<link rel="stylesheet" href="` + html.EscapeString(href) + `">`
	}

	var scripts string
	for _, src := range append([]string{
		"https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/js/bootstrap.bundle.min.js",
		"https://cdn.jsdelivr.net/npm/htmx.org@2.0.4/dist/htmx.min.js",
		"https://cdn.jsdelivr.net/npm/htmx-ext-sse@2.2.2/sse.js",
	}, props.scripts...) {
		scripts += `<script src="` + html.EscapeString(src) + `"></script>`
	}

	for _, js := range props.scriptSnippets {
		scripts += `<script>` + js + `</script>`
	}

	return `
<!DOCTYPE html>
<html lang="en"` + rootAttrs + `>

<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<title>` + titlePrefix + `JitakuDNS</title>
	` + stylesheets + `
</head>

<body class="min-vh-100 d-flex flex-column">
<nav class="navbar navbar-expand-lg bg-body-tertiary">
	<div class="container-fluid">
		<a class="navbar-brand" href="#">JitakuDNS</a>
		<button class="navbar-toggler" type="button" data-bs-toggle="collapse" data-bs-target="#navbarSupportedContent" aria-controls="navbarSupportedContent" aria-expanded="false" aria-label="Toggle navigation">
			<span class="navbar-toggler-icon"></span>
		</button>
		<div class="collapse navbar-collapse" id="navbarSupportedContent">
			<ul class="navbar-nav me-auto mb-2 mb-lg-0">` + navItemsHtml + `</ul>
		</div>
	</div>
</nav>
<div class="container mt-5 flex-grow-1 d-flex flex-column">` + strings.Join(children, "") + `</div>
` + scripts + `
</body>

</html>
`
}
