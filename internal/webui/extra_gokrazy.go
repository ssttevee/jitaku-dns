//go:build gokrazy

package webui

import (
	"bytes"
	"compress/gzip"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/ssttevee/jitaku-dns/internal/gokrazyutil"
	"github.com/ssttevee/jitaku-dns/internal/update"
)

var gokrazyNavItems = []navItem{
	{
		Name: "Update",
		Path: "/update",
	},
}

func registerGoKrazyRoutes(mux *http.ServeMux) {
	u, _ := gokrazyutil.DashboardURL()
	parsed, _ := url.Parse(u)

	if parsed != nil {
		p, _ := parsed.User.Password()
		if p != "" {
			port := parsed.Port()
			if port == "80" {
				port = ""
			} else {
				port = ":" + port
			}

			gokrazyNavItems = append(gokrazyNavItems, navItem{
				Name: "gokrazy",
				Path: u,
				DynamicPath: func(r *http.Request) string {
					urlcopy := &*parsed
					urlcopy.Host = r.Host + port
					return urlcopy.String()
				},
			})
		}
	}

	var updated bool
	var uploadedBytes []byte

	mux.HandleFunc("GET /update", func(w http.ResponseWriter, r *http.Request) {
		if uploadedBytes != nil {
			if !updated {
				defer func() {
					updated = true
				}()

				if reboot, err := update.UpdateFromGZippedImage(uploadedBytes); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				} else {
					go func() {
						time.Sleep(time.Second)

						if err := reboot(); err != nil {
							log.Printf("reboot failed: %v", err)
						}
					}()
				}
			}

			w.Write([]byte("rebooting, please refresh this page in a few moments"))
			return
		}

		w.Write([]byte(RootLayout(
			RootLayoutProps{
				req: r,
				scriptSnippets: []string{
					`
const fileInput = document.getElementById("image-file");
const updateBtn = document.getElementById("update-btn");
const progressBar = document.getElementById("progress-bar");

fileInput.addEventListener("change", () => {
	updateBtn.disabled = !fileInput.files.length;
});

updateBtn.addEventListener("click", () => {
	const file = fileInput.files[0];
	if (!file) {
		return;
	}

	console.log(file);

	try {
		this.disabled = true;
		progressBar.parentElement.hidden = false;
		updateBtn.parentElement.hidden = true;

		var xhr = new XMLHttpRequest();
		xhr.open("POST", "/update", true);
		xhr.upload.onprogress = function(e) {
			if (e.lengthComputable && e.loaded < e.total) {
				const percent = (e.loaded / e.total) * 100;
				progressBar.style.width = percent + "%";
			} else {
				progressBar.style.width = "100%";
				progressBar.classList.add("progress-bar-striped");
				progressBar.classList.add("progress-bar-animated");
			}
		};
		xhr.onload = () => {
			if (xhr.status === 200) {
				location.reload();
			} else {
				alert("update failed: " + xhr.responseText);
				this.disabled = false;
			}
		};
		xhr.send(file);
	} catch (e) {
		this.disabled = false;
	}
});
`,
				},
			},
			`
<h1>Update</h1>
<div class="row">
<div class="col col-lg-6">
	<div class="card">
		<div class="card-body" enctype="multipart/form-data" action="/update" method="POST">
			<label for="upload" class="form-label">
				<h6>Upload Image</h6>
				<p class="my-0 small text-secondary">Select a custom or pre-downloaded image for a fully offline update.</p>
			</label>
			<div>
				<input id="image-file" type="file" name="image">
				<button id="update-btn" disabled class="btn btn-primary">Update</button>
			</div>
			<div class="progress" role="progressbar" aria-label="Basic example" aria-valuenow="0" aria-valuemin="0" aria-valuemax="100" hidden>
				<div id="progress-bar" class="progress-bar" style="width: 0%"></div>
			</div>
		</div>
	</div>
</div>
<div class="col col-lg-6">
</div>
</div>
`,
		)))
	})

	mux.HandleFunc("POST /update", func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > 1<<27 {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}

		var buf bytes.Buffer
		if _, err := buf.ReadFrom(io.LimitReader(r.Body, 1<<27)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if _, err := gzip.NewReader(bytes.NewBuffer(buf.Bytes())); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		uploadedBytes = buf.Bytes()

		log.Println("update image received successfully", len(uploadedBytes))
	})
}
