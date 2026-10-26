package vulnetixcli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func redeemServer(t *testing.T, status int, body map[string]any) (DeviceLogin, *map[string]string) {
	t.Helper()
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/site/v1/cli/device/token" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return DeviceLogin{BaseURL: srv.URL, Client: srv.Client()}, &got
}

func TestRedeemReturnsTheKeyOnce(t *testing.T) {
	d, sent := redeemServer(t, http.StatusOK, map[string]any{"orgId": "org-1", "apiKey": "org-1:abcdef"})
	org, key, err := d.Redeem(context.Background(), "dev-code")
	if err != nil || org != "org-1" || key != "abcdef" {
		t.Fatalf("Redeem = %q %q %v", org, key, err)
	}
	if (*sent)["device_code"] != "dev-code" {
		t.Fatalf("sent %v", *sent)
	}
}

func TestRedeemRefusesAKeyForAnotherOrg(t *testing.T) {
	d, _ := redeemServer(t, http.StatusOK, map[string]any{"orgId": "org-1", "apiKey": "org-2:abcdef"})
	if _, _, err := d.Redeem(context.Background(), "dev-code"); err == nil {
		t.Fatal("accepted a key whose org differs from the answer's")
	}
}

func TestRedeemNamesWhyItFailed(t *testing.T) {
	for code, want := range map[string]error{
		"authorization_pending": ErrDevicePending,
		"expired_token":         ErrDeviceExpired,
		"access_denied":         ErrDeviceDenied,
	} {
		d, _ := redeemServer(t, http.StatusBadRequest, map[string]any{"error": code})
		if _, _, err := d.Redeem(context.Background(), "x"); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", code, err, want)
		}
	}
	d, _ := redeemServer(t, http.StatusInternalServerError, map[string]any{"error": "server_error"})
	if _, _, err := d.Redeem(context.Background(), "x"); err == nil {
		t.Error("a server error redeemed")
	}
}
