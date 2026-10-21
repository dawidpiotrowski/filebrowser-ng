package fbhttp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/share"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

func TestShareRejectsInvalidExpiryBeforeSaving(t *testing.T) {
	for _, tc := range []struct{ expires, unit string }{
		{"abc", "hours"}, {"-1", "hours"}, {"0", "seconds"},
		{"9223372036854775807", "seconds"}, {"106752", "days"},
		{"18446744073709551616", "minutes"}, {"1", "minute"},
	} {
		t.Run(tc.expires+"-"+tc.unit, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("file"), 0o600); err != nil {
				t.Fatal(err)
			}
			key := []byte("share-expiry-key")
			perm := users.Permissions{Share: true, Download: true}
			st := scopedUserStorage(t, root, perm, key)
			body, _ := json.Marshal(share.CreateBody{Expires: tc.expires, Unit: tc.unit})
			w := securityRequest(t, st, &settings.Server{}, sharePostHandler, "POST", "/file.txt", signToken(t, st, perm, key), strings.NewReader(string(body)))
			if w.Code != http.StatusBadRequest {
				t.Errorf("invalid lifetime accepted: %d %s", w.Code, w.Body)
			}
			if links, err := st.Share.All(); !errors.Is(err, fberrors.ErrNotExist) && (err != nil || len(links) != 0) {
				t.Fatalf("invalid share persisted: %v %v", links, err)
			}
		})
	}
}

func TestShareExpiryAndNewLinkEntropy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := []byte("valid-share-expiry")
	perm := users.Permissions{Share: true, Download: true}
	st := scopedUserStorage(t, root, perm, key)
	token := signToken(t, st, perm, key)
	for _, tc := range []struct {
		unit    string
		seconds int64
	}{{"seconds", 1}, {"minutes", 60}, {"hours", 3600}, {"days", 86400}, {"", 3600}} {
		t.Run(tc.unit, func(t *testing.T) {
			body, _ := json.Marshal(share.CreateBody{Expires: "2", Unit: tc.unit})
			before := time.Now().Unix()
			w := securityRequest(t, st, &settings.Server{}, sharePostHandler, "POST", "/file.txt", token, strings.NewReader(string(body)))
			if w.Code != http.StatusOK {
				t.Fatalf("valid share: %d %s", w.Code, w.Body)
			}
			var link shareResponse
			if err := json.Unmarshal(w.Body.Bytes(), &link); err != nil {
				t.Fatal(err)
			}
			if link.Expire < before+2*tc.seconds || link.Expire > time.Now().Unix()+2*tc.seconds {
				t.Fatalf("wrong expiry %d", link.Expire)
			}
			random, err := base64.RawURLEncoding.DecodeString(link.Hash)
			if err != nil || len(random) < 16 {
				t.Fatalf("new share has less than 128 bits of randomness: %d bytes %v", len(random), err)
			}
		})
	}
	w := securityRequest(t, st, &settings.Server{}, sharePostHandler, "POST", "/file.txt", token, strings.NewReader(`{}`))
	var permanent shareResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &permanent) != nil || permanent.Expire != 0 {
		t.Fatalf("explicit no-expiry behavior changed: %d %s", w.Code, w.Body)
	}
}

func TestSharePasswordTooLongIsAClientError(t *testing.T) {
	if _, status, err := getSharePasswordHash(share.CreateBody{Password: strings.Repeat("x", 73)}); err == nil || status != http.StatusBadRequest {
		t.Fatalf("expected invalid password to return 400, got %d %v", status, err)
	}
}
