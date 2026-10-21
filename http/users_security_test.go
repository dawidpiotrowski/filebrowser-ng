package fbhttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/auth"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/storage"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
	"github.com/gorilla/mux"
	"golang.org/x/crypto/bcrypt"
)

const auditPassword = "CurrentPassword123!"

func userUpdateFixture(t *testing.T, admin bool) (*storage.Storage, string) {
	t.Helper()
	key := []byte("user-update-key")
	st := scopedUserStorage(t, t.TempDir(), users.Permissions{Admin: admin}, key)
	hash, err := bcrypt.GenerateFromPassword([]byte(auditPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.Users.Get("", false, uint(1))
	if err != nil {
		t.Fatal(err)
	}
	u.Password = string(hash)
	if err := st.Users.Update(u, "Password"); err != nil {
		t.Fatal(err)
	}
	if err := st.Settings.Save(&settings.Settings{Key: key, AuthMethod: auth.MethodJSONAuth}); err != nil {
		t.Fatal(err)
	}
	return st, signToken(t, st, users.Permissions{}, key)
}

func updateUserRequest(t *testing.T, st *storage.Storage, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := mux.SetURLVars(httptest.NewRequest("PUT", "/users/1", bytes.NewReader(b)), map[string]string{"id": "1"})
	r.Header.Set("X-Auth", token)
	w := httptest.NewRecorder()
	handle(userPutHandler, "", st, &settings.Server{}).ServeHTTP(w, r)
	return w
}

func TestUserSensitiveUpdatesRequireCurrentPassword(t *testing.T) {
	for _, which := range [][]string{nil, {}, {"all"}, {"lockPassword"}, {"rules"}} {
		name, _ := json.Marshal(which)
		t.Run(string(name), func(t *testing.T) {
			st, token := userUpdateFixture(t, true)
			w := updateUserRequest(t, st, token, map[string]any{
				"what": "user", "which": which,
				"data": users.User{ID: 1, Username: "u", Password: "ChangedPassword123!", LockPassword: true, Perm: users.Permissions{Admin: true}},
			})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("sensitive update without current password: %d %s", w.Code, w.Body)
			}
			u, err := st.Users.Get("", false, uint(1))
			if err != nil || !users.CheckPwd(auditPassword, u.Password) || u.LockPassword {
				t.Fatalf("rejected update changed credentials: %v", err)
			}
		})
	}
}

func TestUserUpdatesValidateEveryFieldBeforeWriting(t *testing.T) {
	for _, which := range [][]string{{"locale", "doesNotExist"}, {"ID"}, {"Fs"}, {"ALL"}} {
		name, _ := json.Marshal(which)
		t.Run(string(name), func(t *testing.T) {
			st, token := userUpdateFixture(t, false)
			w := updateUserRequest(t, st, token, map[string]any{
				"what": "user", "which": which,
				"data": users.User{ID: 1, Locale: "pl"},
			})
			if w.Code != http.StatusBadRequest && w.Code != http.StatusForbidden {
				t.Errorf("invalid update: %d %s", w.Code, w.Body)
			}
			u, err := st.Users.Get("", false, uint(1))
			if err != nil || u.Locale != "" {
				t.Fatalf("invalid request partially updated user: locale=%q err=%v", u.Locale, err)
			}
		})
	}
}

func TestUserFullUpdateEnforcesShareDownloadInvariant(t *testing.T) {
	st, token := userUpdateFixture(t, true)
	w := updateUserRequest(t, st, token, map[string]any{
		"what": "user", "current_password": auditPassword,
		"data": users.User{ID: 1, Username: "u", Perm: users.Permissions{Admin: true, Share: true}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("share without download: %d %s", w.Code, w.Body)
	}
}

func TestUserUpdateRejectsMissingData(t *testing.T) {
	st, token := userUpdateFixture(t, true)
	w := updateUserRequest(t, st, token, map[string]any{"what": "user", "which": []string{"locale"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing data: %d %s", w.Code, w.Body)
	}
}

func TestUserPreferencesAndAuthorizedPasswordUpdateStillWork(t *testing.T) {
	st, token := userUpdateFixture(t, false)
	w := updateUserRequest(t, st, token, map[string]any{
		"what": "user", "which": []string{"locale", "viewMode"},
		"data": users.User{ID: 1, Locale: "pl", ViewMode: users.MosaicViewMode},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("preferences: %d %s", w.Code, w.Body)
	}
	w = updateUserRequest(t, st, token, map[string]any{
		"what": "user", "which": []string{"password"}, "current_password": auditPassword,
		"data": users.User{ID: 1, Password: "ChangedPassword123!"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("password: %d %s", w.Code, w.Body)
	}
	u, err := st.Users.Get("", false, uint(1))
	if err != nil || !users.CheckPwd("ChangedPassword123!", u.Password) || u.Locale != "pl" {
		t.Fatalf("authorized update failed: %v", err)
	}
}
