package webui

import (
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/miekg/dns"
	"github.com/ssttevee/jitaku-dns/internal"
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
	ForwardStrategy() upstream.ForwardStrategy
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
		currentStrategy := upstream.DefaultStrategy.Enum()
		if s := c.ForwardStrategy(); s != nil {
			currentStrategy = s.Enum()
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

		w.Write([]byte(RootLayout(
			RootLayoutProps{
				title:    "Settings",
				pathname: r.URL.Path,
			},
			"<h1>Settings</h1>",
			`
<div class="container my-5">
<h3>DNS</h3>
<hr/>
<div class="container my-5">
<div class="row">
	<div class="col">
		<div class="card">
			<div class="card-body">
				<div class="mb-3">
					<label for="servers-text-area" class="form-label">
						<h6>Servers</h6>
						<p class="my-0 small">Enter one server address per line.</p>
					</label>
					<textarea class="form-control" id="servers-text-area" rows="3"></textarea>
				</div>
				<fieldset>
					<legend><h6>Strategy</h6></legend>
					<p class="mt-0 small">How to select upstream server.</p>
					<div class="container">
						<div class="row">`+radioOptions+`</div>
					</div>
				</fieldset>
			</div>
		</div>
	</div>
	<div class="col">
		<div class="card">
			<div class="card-body">
				<label for="rewrites-text-area" class="form-label">
					<h6>Rewrites</h6>
					<p class="my-0 small">Enter in the format of <code>/etc/hosts</code>.</p>
				</label>
				<textarea class="form-control" id="rewrites-text-area" rows="3"></textarea>
			</div>
		</div>
	</div>
</div>
</div>
</div>
`,
		)))
	})
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
	title    string
	pathname string
	darkmode bool
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

	type NavItem struct {
		Name string
		Path string
	}

	navItems := []NavItem{
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
	}

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

	return `
<!DOCTYPE html>
<html lang="en"` + rootAttrs + `>

<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<title>` + titlePrefix + `JitakuDNS</title>
	<link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/css/bootstrap.min.css" rel="stylesheet" integrity="sha384-QWTKZyjpPEjISv5WaRU9OFeRpok6YctnYmDr5pNlyT2bRjXh0JMhjY6hW+ALEwIH" crossorigin="anonymous">
</head>

<body>
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
<div class="container my-5">` + strings.Join(children, "") + `</div>
<script src="https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/js/bootstrap.bundle.min.js" integrity="sha384-YvpcrYf0tY3lHB60NNkmXc5s9fDVZLESaAA55NDzOxhy9GkcIdslK1eN7N6jIeHz" crossorigin="anonymous"></script>
<script src="https://cdn.jsdelivr.net/npm/htmx.org@2.0.4/dist/htmx.min.js" integrity="sha256-4gndpcgjVHnzFm3vx3UOHbzVpcGAi3eS/C5nM3aPtEc=" crossorigin="anonymous"></script>
<script src="https://cdn.jsdelivr.net/npm/htmx-ext-sse@2.2.2/sse.js" integrity="sha256-g+ym+gYR/isL8XALQkuIteztOO9Ejvl2Ci6gj7yHVhE=" crossorigin="anonymous"></script>
</body>

</html>
`
}
