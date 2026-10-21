package share

type CreateBody struct {
	Password string `json:"password"`
	Expires  string `json:"expires"`
	Unit     string `json:"unit"`
}

// Link is the information needed to build a shareable link.
type Link struct {
	Hash         string `json:"hash" storm:"id,index"`
	Path         string `json:"path" storm:"index"`
	UserID       uint   `json:"userID"`
	Expire       int64  `json:"expire"`
	PasswordHash string `json:"password_hash,omitempty"`
	// Token retains the historical per-share secret for database compatibility.
	// Query-string authentication is disabled; this value binds short-lived
	// password grants so rotating it invalidates previously issued grants.
	Token string `json:"token,omitempty"`
}
