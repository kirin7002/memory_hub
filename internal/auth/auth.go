package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Principal struct {
	Tenant  string `json:"tenant_id"`
	User    string `json:"user_id"`
	Device  string `json:"device_id"`
	Agent   string `json:"agent_id"`
	Expires int64  `json:"expires_at"`
}

// Sign issues development credentials. Production identity providers are separate.
func Sign(key []byte, p Principal) string {
	b, _ := json.Marshal(p)
	body := base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("memoryhub:dev-auth:" + body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func Verify(key []byte, token string) (Principal, error) {
	var p Principal
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return p, errors.New("invalid token")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return p, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("memoryhub:dev-auth:" + parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return p, errors.New("invalid signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if p.Tenant == "" || p.User == "" || p.Device == "" || p.Agent == "" || p.Expires <= time.Now().Unix() {
		return p, errors.New("expired or incomplete token")
	}
	return p, nil
}
