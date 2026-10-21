package fbhttp

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/auth"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/storage"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/version"
	"github.com/spf13/afero"
)

func handleWithStaticData(w http.ResponseWriter, _ *http.Request, d *data, fSys fs.FS, file, contentType string) (int, error) {
	w.Header().Set("Content-Type", contentType)

	auther, err := d.store.Auth.Get(d.settings.AuthMethod)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	data := map[string]interface{}{
		"Name":                  d.settings.Branding.Name,
		"DisableExternal":       d.settings.Branding.DisableExternal,
		"DisableUsedPercentage": d.settings.Branding.DisableUsedPercentage,
		"Color":                 d.settings.Branding.Color,
		"BaseURL":               d.server.BaseURL,
		"Version":               version.Version,
		"StaticURL":             path.Join(d.server.BaseURL, "/static"),
		"Signup":                d.settings.Signup,
		"NoAuth":                d.settings.AuthMethod == auth.MethodNoAuth,
		"AuthMethod":            d.settings.AuthMethod,
		"LogoutPage":            d.settings.LogoutPage,
		"LoginPage":             auther.LoginPage(),
		"CSS":                   false,
		"ReCaptcha":             false,
		"Theme":                 d.settings.Branding.Theme,
		"EnableThumbs":          d.server.EnableThumbnails,
		"ResizePreview":         d.server.ResizePreview,
		"EnableExec":            false,
		"TusSettings":           d.settings.Tus,
		"HideLoginButton":       d.settings.HideLoginButton,
	}

	if d.settings.Branding.Files != "" {
		fPath := filepath.Join(d.settings.Branding.Files, "custom.css")
		_, err := os.Stat(fPath)

		if err != nil && !os.IsNotExist(err) {
			log.Printf("couldn't load custom styles: %v", err)
		}

		if err == nil {
			data["CSS"] = true
		}
	}

	if d.settings.AuthMethod == auth.MethodJSONAuth {
		raw, err := d.store.Auth.Get(d.settings.AuthMethod)
		if err != nil {
			return http.StatusInternalServerError, err
		}

		auther := raw.(*auth.JSONAuth)

		if auther.ReCaptcha != nil {
			data["ReCaptcha"] = auther.ReCaptcha.Key != "" && auther.ReCaptcha.Secret != ""
			data["ReCaptchaHost"] = auther.ReCaptcha.Host
			data["ReCaptchaKey"] = auther.ReCaptcha.Key
		}
	}

	b, err := json.Marshal(data)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	// Let html/template serialize JSON in JavaScript context without trusting raw JS.
	data["Json"] = json.RawMessage(b)

	fileContents, err := fs.ReadFile(fSys, file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return http.StatusNotFound, err
		}
		return http.StatusInternalServerError, err
	}
	index := template.Must(template.New("index").Delims("[{[", "]}]").Parse(string(fileContents)))
	err = index.Execute(w, data)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	return 0, nil
}

func getStaticHandlers(store *storage.Storage, server *settings.Server, assetsFs fs.FS) (index, static http.Handler) {
	index = handle(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if r.Method != http.MethodGet {
			return http.StatusNotFound, nil
		}

		w.Header().Set("x-xss-protection", "1; mode=block")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		return handleWithStaticData(w, r, d, assetsFs, "public/index.html", "text/html; charset=utf-8")
	}, "", store, server)

	static = handle(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if r.Method != http.MethodGet {
			return http.StatusNotFound, nil
		}

		if strings.HasSuffix(r.URL.Path, "/") {
			return http.StatusNotFound, nil
		}

		const maxAge = 86400 // 1 day
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%v", maxAge))
		w.Header().Set("X-Content-Type-Options", "nosniff")

		if d.settings.Branding.Files != "" && (strings.HasPrefix(r.URL.Path, "img/") || r.URL.Path == "custom.css") {
			served, err := serveBrandingFile(w, r, d.settings.Branding.Files, r.URL.Path)
			if err != nil {
				return errToStatus(err), err
			}
			if served {
				return 0, nil
			}
		}

		if !strings.HasSuffix(r.URL.Path, ".js") {
			http.FileServer(http.FS(assetsFs)).ServeHTTP(w, r)
			return 0, nil
		}

		f, err := assetsFs.Open(r.URL.Path + ".gz")
		if err != nil {
			return http.StatusNotFound, err
		}
		defer f.Close()

		acceptEncoding := r.Header.Get("Accept-Encoding")
		if strings.Contains(acceptEncoding, "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")

			if _, err := io.Copy(w, f); err != nil {
				return http.StatusInternalServerError, err
			}
		} else {
			gzReader, err := gzip.NewReader(f)
			if err != nil {
				return http.StatusInternalServerError, err
			}
			defer gzReader.Close()

			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")

			if _, err := io.Copy(w, gzReader); err != nil {
				return http.StatusInternalServerError, err
			}
		}

		return 0, nil
	}, "/static/", store, server)

	return index, static
}

// Branding overrides use the same confined filesystem as user content. This
// also rejects Windows backslash traversal before a host filesystem sees it.
func serveBrandingFile(w http.ResponseWriter, r *http.Request, root, name string) (bool, error) {
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		return false, os.ErrPermission
	}
	branding := files.NewScopedFs(afero.NewOsFs(), root)
	f, err := branding.Open(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, os.ErrPermission
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	return true, nil
}
