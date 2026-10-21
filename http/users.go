package fbhttp

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"github.com/dawidpiotrowski/filebrowser-ng/v2/auth"
	fberrors "github.com/dawidpiotrowski/filebrowser-ng/v2/errors"
	"github.com/dawidpiotrowski/filebrowser-ng/v2/users"
)

type userUpdateField struct {
	name             string
	adminOnly        bool
	passwordRequired bool
}

// Only persisted, intentionally editable fields belong in this list. In
// particular ID and the runtime filesystem must never reach reflection-based
// storage updates. Normalize once, before authorization or any writes.
var userUpdateFields = map[string]userUpdateField{
	"username":              {"Username", true, true},
	"password":              {"Password", false, true},
	"scope":                 {"Scope", true, true},
	"lockpassword":          {"LockPassword", true, true},
	"perm":                  {"Perm", true, true},
	"commands":              {"Commands", true, true},
	"rules":                 {"Rules", true, true},
	"locale":                {name: "Locale"},
	"viewmode":              {name: "ViewMode"},
	"singleclick":           {name: "SingleClick"},
	"redirectaftercopymove": {name: "RedirectAfterCopyMove"},
	"sorting":               {name: "Sorting"},
	"hidedotfiles":          {name: "HideDotfiles"},
	"dateformat":            {name: "DateFormat"},
	"aceeditortheme":        {name: "AceEditorTheme"},
}

type modifyUserRequest struct {
	modifyRequest
	Data *users.User `json:"data"`
}

func getUserID(r *http.Request) (uint, error) {
	vars := mux.Vars(r)
	i, err := strconv.ParseUint(vars["id"], 10, 0)
	if err != nil {
		return 0, err
	}
	return uint(i), err
}

func getUser(w http.ResponseWriter, r *http.Request) (*modifyUserRequest, error) {
	if r.Body == nil {
		return nil, fberrors.ErrEmptyRequest
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodySize)
	req := &modifyUserRequest{}
	err := json.NewDecoder(r.Body).Decode(req)
	if err != nil {
		return nil, err
	}

	if req.What != "user" || req.Data == nil {
		return nil, fberrors.ErrInvalidDataType
	}

	return req, nil
}

func withSelfOrAdmin(fn handleFunc) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		id, err := getUserID(r)
		if err != nil {
			return http.StatusInternalServerError, err
		}

		if d.user.ID != id && !d.user.Perm.Admin {
			return http.StatusForbidden, nil
		}

		d.raw = id
		return fn(w, r, d)
	})
}

var usersGetHandler = withAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	users, err := d.store.Users.Gets(d.server.Root, d.server.FollowExternalSymlinks)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	for _, u := range users {
		u.Password = ""
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].ID < users[j].ID
	})

	return renderJSON(w, r, users)
})

var userGetHandler = withSelfOrAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	u, err := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, d.raw.(uint))
	if errors.Is(err, fberrors.ErrNotExist) {
		return http.StatusNotFound, err
	}

	if err != nil {
		return http.StatusInternalServerError, err
	}

	u.Password = ""
	if !d.user.Perm.Admin {
		u.Scope = ""
	}
	return renderJSON(w, r, u)
})

var userDeleteHandler = withSelfOrAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if r.Body == nil {
		return http.StatusBadRequest, fberrors.ErrEmptyRequest
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodySize)
	var body struct {
		CurrentPassword string `json:"current_password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return http.StatusBadRequest, err
	}

	if d.settings.AuthMethod == auth.MethodJSONAuth {
		if !users.CheckPwd(body.CurrentPassword, d.user.Password) {
			return http.StatusBadRequest, fberrors.ErrCurrentPasswordIncorrect
		}
	}

	err := d.store.Users.Delete(d.raw.(uint))
	if err != nil {
		return errToStatus(err), err
	}

	return http.StatusOK, nil
})

var userPostHandler = withAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	req, err := getUser(w, r)
	if err != nil {
		return http.StatusBadRequest, err
	}

	if d.settings.AuthMethod == auth.MethodJSONAuth {
		if !users.CheckPwd(req.CurrentPassword, d.user.Password) {
			return http.StatusBadRequest, fberrors.ErrCurrentPasswordIncorrect
		}
	}

	if len(req.Which) != 0 {
		return http.StatusBadRequest, nil
	}

	if req.Data.Password == "" {
		return http.StatusBadRequest, fberrors.ErrEmptyPassword
	}

	req.Data.Password, err = users.ValidateAndHashPwd(req.Data.Password, d.settings.MinimumPasswordLength)
	if err != nil {
		return http.StatusBadRequest, err
	}

	if req.Data.Perm.Share && !req.Data.Perm.Download {
		return http.StatusBadRequest, fberrors.ErrShareRequiresDownload
	}

	userHome, err := d.settings.MakeUserDir(req.Data.Username, req.Data.Scope, d.server.Root)
	if err != nil {
		log.Printf("create user: failed to mkdir user home dir: [%s]", userHome)
		return http.StatusInternalServerError, err
	}
	req.Data.Scope = userHome
	log.Printf("user: %s, home dir: [%s].", req.Data.Username, userHome)

	err = d.store.Users.Save(req.Data)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	w.Header().Set("Location", "/settings/users/"+strconv.FormatUint(uint64(req.Data.ID), 10))
	return http.StatusCreated, nil
})

var userPutHandler = withSelfOrAdmin(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	req, err := getUser(w, r)
	if err != nil {
		return http.StatusBadRequest, err
	}

	if req.Data.ID != d.raw.(uint) {
		return http.StatusBadRequest, nil
	}

	fullUpdate := len(req.Which) == 0 || (len(req.Which) == 1 && strings.EqualFold(req.Which[0], "all"))
	passwordRequired := fullUpdate
	updatesPassword, updatesPerm := fullUpdate, fullUpdate
	if fullUpdate {
		if !d.user.Perm.Admin {
			return http.StatusForbidden, nil
		}
		req.Which = nil
	} else {
		for i, name := range req.Which {
			field, ok := userUpdateFields[strings.ToLower(name)]
			if !ok {
				return http.StatusBadRequest, fberrors.ErrInvalidOption
			}
			if field.adminOnly && !d.user.Perm.Admin {
				return http.StatusForbidden, nil
			}
			req.Which[i] = field.name
			passwordRequired = passwordRequired || field.passwordRequired
			updatesPassword = updatesPassword || field.name == "Password"
			updatesPerm = updatesPerm || field.name == "Perm"
		}
	}

	if d.settings.AuthMethod == auth.MethodJSONAuth && passwordRequired && !users.CheckPwd(req.CurrentPassword, d.user.Password) {
		return http.StatusBadRequest, fberrors.ErrCurrentPasswordIncorrect
	}
	if updatesPerm && req.Data.Perm.Share && !req.Data.Perm.Download {
		return http.StatusBadRequest, fberrors.ErrShareRequiresDownload
	}

	if updatesPassword {
		if !d.user.Perm.Admin && d.user.LockPassword {
			return http.StatusForbidden, nil
		}
		if fullUpdate && req.Data.Password == "" {
			stored, getErr := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, d.raw.(uint))
			if getErr != nil {
				return http.StatusInternalServerError, getErr
			}
			req.Data.Password = stored.Password
		} else {
			req.Data.Password, err = users.ValidateAndHashPwd(req.Data.Password, d.settings.MinimumPasswordLength)
			if err != nil {
				return http.StatusBadRequest, err
			}
		}
	}

	err = d.store.Users.Update(req.Data, req.Which...)
	if err != nil {
		return errToStatus(err), err
	}

	return http.StatusOK, nil
})
