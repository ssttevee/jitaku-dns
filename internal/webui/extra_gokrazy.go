//go:build gokrazy

package webui

import (
	"net/http"

	"github.com/ssttevee/jitaku-dns/internal/update"
)

var gokrazyNavItems = []navItem{
	{
		Name: "Update",
		Path: "/update",
	},
}

func registerGoKrazyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /update", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(RootLayout(
			RootLayoutProps{
				pathname: r.URL.Path,
			},
			`
<h1>Update</h1>
<div class="row">
<div class="col col-lg-6">
	<div class="card">
		<form class="card-body" enctype="multipart/form-data" action="/update" method="POST">
			<label for="upload" class="form-label">
				<h6>Upload Image</h6>
				<p class="my-0 small text-secondary">Select a custom or pre-downloaded image for a fully offline update.</p>
			</label>
			<input type="file" name="image">
			<button class="btn btn-primary">Update</button>
		</form>
	</div>
</div>
<div class="col col-lg-6">
	<div class="card">
		<div class="card-body">
		</div>
	</div>
</div>
</div>
`,
		)))
	})

	mux.HandleFunc("POST /update", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 27); err != nil { // 128 MiB
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		for _, fh := range r.MultipartForm.File["image"] {
			f, err := fh.Open()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			if err := update.UpdateFromGZippedImage(f); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			http.Redirect(w, r, "/update", http.StatusSeeOther)
			return
		}

		http.Error(w, "", http.StatusBadRequest)
	})
}
