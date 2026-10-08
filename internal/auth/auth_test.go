package auth

import (
	"strings"
	"testing"
	"time"
)

// TestRejectTamperedExpiredAndMissingIdentity 验证开发凭证完整性、有效期和设备绑定。
// 此处直接调用鉴权函数；对应所有 /v1 接口的 Authorization: Bearer 校验，不发 HTTP 请求。
func TestRejectTamperedExpiredAndMissingIdentity(t *testing.T) {
	key := []byte("test-only-auth-secret-32-bytes-or-more")
	p := Principal{Tenant: "t", User: "u", Device: "phone", Agent: "agent", Expires: time.Now().Add(time.Hour).Unix()}
	token := Sign(key, p)
	if _, err := Verify(key, token); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	parts[0] = "e30"
	if _, err := Verify(key, strings.Join(parts, ".")); err == nil {
		t.Fatal("accepted tampered identity")
	}
	p.Expires = time.Now().Add(-time.Minute).Unix()
	if _, err := Verify(key, Sign(key, p)); err == nil {
		t.Fatal("accepted expired token")
	}
	p.Expires = time.Now().Add(time.Hour).Unix()
	p.Device = ""
	if _, err := Verify(key, Sign(key, p)); err == nil {
		t.Fatal("accepted unbound device")
	}
}
