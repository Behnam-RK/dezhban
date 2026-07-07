package vpnimport

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractWireGuard(t *testing.T) {
	body := `[Interface]
PrivateKey = SECRETKEYSHOULDNOTBEREAD=
Address = 10.0.0.2/32

[Peer]
PublicKey = PUBKEY=
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0`
	eps, format, err := Extract(writeTemp(t, "wg0.conf", body))
	if err != nil {
		t.Fatal(err)
	}
	if format != "wireguard" {
		t.Errorf("format = %q, want wireguard", format)
	}
	if len(eps) != 1 || eps[0] != "vpn.example.com" {
		t.Errorf("endpoints = %v, want [vpn.example.com] (port stripped)", eps)
	}
}

func TestExtractOpenVPN(t *testing.T) {
	body := `client
dev tun
remote us1.example.com 1194 udp
remote 203.0.113.9 443 tcp
cipher AES-256-GCM`
	eps, format, err := Extract(writeTemp(t, "client.ovpn", body))
	if err != nil {
		t.Fatal(err)
	}
	if format != "openvpn" {
		t.Errorf("format = %q, want openvpn", format)
	}
	sort.Strings(eps)
	want := []string{"203.0.113.9", "us1.example.com"}
	if len(eps) != 2 || eps[0] != want[0] || eps[1] != want[1] {
		t.Errorf("endpoints = %v, want %v", eps, want)
	}
}

func TestExtractV2RayJSON(t *testing.T) {
	body := `{
	  "outbounds": [
	    {"protocol": "vmess", "settings": {"vnext": [
	      {"address": "cdn.example.com", "port": 443, "users": [{"id": "SECRET"}]}
	    ]}},
	    {"protocol": "shadowsocks", "servers": [
	      {"address": "198.51.100.20", "server_port": 8388, "password": "SECRET"}
	    ]},
	    {"protocol": "freedom"}
	  ]
	}`
	eps, format, err := Extract(writeTemp(t, "config.json", body))
	if err != nil {
		t.Fatal(err)
	}
	if format != "v2ray" {
		t.Errorf("format = %q, want v2ray", format)
	}
	sort.Strings(eps)
	if len(eps) != 2 || eps[0] != "198.51.100.20" || eps[1] != "cdn.example.com" {
		t.Errorf("endpoints = %v, want [198.51.100.20 cdn.example.com]", eps)
	}
}

func TestExtractFiltersPrivateAndLoopback(t *testing.T) {
	body := `[Peer]
Endpoint = 10.0.0.1:51820
` // private → filtered out, nothing usable left
	if _, _, err := Extract(writeTemp(t, "lan.conf", body)); err == nil {
		t.Error("expected error when only private endpoints are present")
	}
}

func TestExtractGarbage(t *testing.T) {
	if _, _, err := Extract(writeTemp(t, "junk.txt", "hello world")); err == nil {
		t.Error("expected error for unrecognized format")
	}
}
