package fbhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/rules"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/settings"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

func TestSettingsRejectInvalidRegexRulesWithoutPersisting(t *testing.T) {
	for _, rule := range []string{`{"regex":true,"allow":false}`, `{"regex":true,"allow":false,"regexp":{"raw":"["}}`} {
		t.Run(rule, func(t *testing.T) {
			st, token := userUpdateFixture(t, true)
			r := httptest.NewRequest("PUT", "/settings", strings.NewReader(`{"rules":[`+rule+`],"branding":{"name":"must-not-save"}}`))
			r.Header.Set("X-Auth", token)
			w := httptest.NewRecorder()
			handle(settingsPutHandler, "", st, &settings.Server{}).ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("invalid rules saved: %d %s", w.Code, w.Body)
			}
			stored, err := st.Settings.Get()
			if err != nil || len(stored.Rules) != 0 || stored.Branding.Name != "" {
				t.Fatalf("rejected settings persisted: %v", err)
			}
		})
	}
}

func TestExistingInvalidRulesDenyAccessWithoutPanic(t *testing.T) {
	for _, rule := range []rules.Rule{{Regex: true}, {Regex: true, Regexp: &rules.Regexp{Raw: "["}}} {
		func() {
			defer func() {
				if err := recover(); err != nil {
					t.Errorf("malformed rule panicked: %v", err)
				}
			}()
			d := &data{settings: &settings.Settings{Rules: []rules.Rule{rule, {Allow: true, Path: "/"}}}, server: &settings.Server{}, user: &users.User{}}
			if d.CheckRules("/private.txt") {
				t.Error("invalid rule must fail closed even if followed by an allow rule")
			}
		}()
	}
}
