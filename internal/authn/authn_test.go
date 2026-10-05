package authn

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestTokenRequiresBearerScheme(t *testing.T) {
	a, err := New(context.Background(), Config{Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		header string
		want   bool
	}{{"Bearer secret", true}, {"secret", false}, {"Bearer wrong", false}, {"", false}} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", tc.header)
		if a(r) != tc.want {
			t.Fatalf("header %q accepted incorrectly", tc.header)
		}
	}
}
func TestBasicAuthAndTrustedProxy(t *testing.T) {
	a, err := New(context.Background(), Config{BasicUsername: "sre", BasicPassword: "private"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetBasicAuth("sre", "private")
	if !a(r) {
		t.Fatal("valid basic rejected")
	}
	r.SetBasicAuth("sre", "wrong")
	if a(r) {
		t.Fatal("wrong basic accepted")
	}
	a, err = New(context.Background(), Config{ProxyHeader: "X-Forwarded-User", TrustedProxyCIDRs: []string{"127.0.0.1/32"}})
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Forwarded-User", "alice")
	r.RemoteAddr = "203.0.113.1:8000"
	if a(r) {
		t.Fatal("untrusted client spoofed identity")
	}
	r.RemoteAddr = "127.0.0.1:9000"
	if !a(r) {
		t.Fatal("trusted proxy rejected")
	}
}
func TestIncompleteAuthConfigurationFailsClosed(t *testing.T) {
	for _, c := range []Config{{BasicUsername: "user"}, {OIDCIssuer: "https://identity.example"}, {ProxyHeader: "X-User"}} {
		if _, err := New(context.Background(), c); err == nil {
			t.Fatalf("accepted incomplete %+v", c)
		}
	}
}
