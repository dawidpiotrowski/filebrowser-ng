package fbhttp

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"errors"
	"github.com/golang-jwt/jwt/v5"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/files"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/share"
	"golang.org/x/crypto/bcrypt"
)

var withHashFile = func(fn handleFunc) handleFunc {
	return func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		id, ifPath := ifPathWithName(r)
		link, err := d.store.Share.GetByHash(id)
		if err != nil {
			return errToStatus(err), err
		}

		status, err := authenticateShareRequest(w, r, d, link)
		if status != 0 || err != nil {
			return status, err
		}

		user, err := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, link.UserID)
		if err != nil {
			return errToStatus(err), err
		}

		if !user.Perm.Share || !user.Perm.Download {
			return http.StatusForbidden, nil
		}

		d.user = user

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       link.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			CalcImgRes: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		if err != nil {
			return errToStatus(err), err
		}

		// share base path. Canonicalized because it roots both the rebased
		// filesystem and checkerPrefix below, and a stored path that is not
		// "/"-separated would make the two disagree on Windows.
		basePath := slashClean(link.Path)

		// file relative path
		filePath := ""

		if file.IsDir {
			filePath = ifPath
		}

		// set fs root to the shared file/folder. Unless external symlinks are
		// explicitly allowed, this is a ScopedFs (not a bare BasePathFs) so the
		// share is also symlink-confined: a link inside the shared subtree that
		// points elsewhere in the owner's scope — outside the share — must not be
		// followed.
		d.user.Fs = files.NewFs(d.user.Fs, basePath, d.server.FollowExternalSymlinks)

		// the filesystem is now rebased onto basePath, so paths handed to the
		// rule checker are relative to it. Resolve them back to the user's
		// original scope so deny rules below the share root keep applying.
		d.checkerPrefix = basePath

		file, err = files.NewFileInfo(&files.FileOptions{
			Fs:      d.user.Fs,
			Path:    filePath,
			Modify:  d.user.Perm.Modify,
			Expand:  true,
			Checker: d,
		})
		if err != nil {
			return errToStatus(err), err
		}

		if file.IsDir {
			// extract name from the last directory in the path
			name := filepath.Base(strings.TrimRight(link.Path, string(filepath.Separator)))
			file.Name = name
		}

		d.raw = file
		return fn(w, r, d)
	}
}

// ref to https://github.com/filebrowser/filebrowser/pull/727
// `/api/public/dl/MEEuZK-v/file-name.txt` for old browsers to save file with correct name
func ifPathWithName(r *http.Request) (id, filePath string) {
	pathElements := strings.Split(r.URL.Path, "/")
	// prevent maliciously constructed parameters like `/api/public/dl/XZzCDnK2_not_exists_hash_name`
	// len(pathElements) will be 1, and golang will panic `runtime error: index out of range`

	switch len(pathElements) {
	case 1:
		return r.URL.Path, "/"
	default:
		// Public share routes do not pass through withUser, so canonicalize the
		// share-relative path here instead.
		return pathElements[0], slashClean(path.Join(pathElements[1:]...))
	}
}

var publicShareHandler = withHashFile(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	file := d.raw.(*files.FileInfo)

	if file.IsDir {
		file.Sorting = files.Sorting{By: "name", Asc: false}
		file.ApplySort()
		return renderJSON(w, r, file)
	}

	return renderJSON(w, r, file)
})

var publicDlHandler = withHashFile(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	file := d.raw.(*files.FileInfo)
	if !file.IsDir {
		return rawFileHandler(w, r, file)
	}

	return rawDirHandler(w, r, d, file)
})

const shareGrantLifetime = 15 * time.Minute

func shareCookieName(hash string) string {
	sum := sha256.Sum256([]byte(hash))
	return "fb_share_" + hex.EncodeToString(sum[:8])
}

func shareCredential(l *share.Link) string {
	sum := sha256.Sum256([]byte(l.PasswordHash + "\x00" + l.Token))
	return hex.EncodeToString(sum[:])
}

func authenticateShareRequest(w http.ResponseWriter, r *http.Request, d *data, l *share.Link) (int, error) {
	if l.PasswordHash == "" {
		return 0, nil
	}
	// Legacy permanent query-string bypass tokens are deliberately ignored.
	if cookie, err := r.Cookie(shareCookieName(l.Hash)); err == nil {
		claims := &jwt.RegisteredClaims{}
		token, err := jwt.ParseWithClaims(cookie.Value, claims, func(_ *jwt.Token) (interface{}, error) { return d.settings.Key, nil },
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(),
			jwt.WithIssuer("filebrowser-ng/share"), jwt.WithAudience(l.Hash), jwt.WithSubject(shareCredential(l)))
		if err == nil && token.Valid {
			return 0, nil
		}
	}
	password, err := url.QueryUnescape(r.Header.Get("X-SHARE-PASSWORD"))
	if err != nil {
		return http.StatusBadRequest, err
	}
	if password == "" {
		return http.StatusUnauthorized, nil
	}
	if !allowAuthentication(r.RemoteAddr, time.Now()) {
		return http.StatusTooManyRequests, nil
	}
	select {
	case passwordWork <- struct{}{}:
		defer func() { <-passwordWork }()
	default:
		return http.StatusTooManyRequests, nil
	}
	if err := bcrypt.CompareHashAndPassword([]byte(l.PasswordHash), []byte(password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return http.StatusUnauthorized, nil
		}
		return http.StatusUnauthorized, err
	}
	expires := time.Now().Add(shareGrantLifetime)
	if l.Expire > 0 && time.Unix(l.Expire, 0).Before(expires) {
		expires = time.Unix(l.Expire, 0)
	}
	claims := &jwt.RegisteredClaims{Issuer: "filebrowser-ng/share", Subject: shareCredential(l), Audience: jwt.ClaimStrings{l.Hash}, ExpiresAt: jwt.NewNumericDate(expires)}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(d.settings.Key)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	http.SetCookie(w, &http.Cookie{Name: shareCookieName(l.Hash), Value: signed,
		Path: strings.TrimRight(d.server.BaseURL, "/") + "/api/public/", Expires: expires,
		HttpOnly: true, Secure: secureBrowserRequest(r, d), SameSite: http.SameSiteStrictMode})
	return 0, nil
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"OK"}`))
}
