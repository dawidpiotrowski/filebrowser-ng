package fbhttp

import "net/http"

// Commands cannot be safely confined to a user's filesystem scope.
var commandsHandler = withUser(func(_ http.ResponseWriter, _ *http.Request, _ *data) (int, error) {
	return http.StatusGone, nil
})
