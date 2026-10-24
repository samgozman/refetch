package main

import (
	"testing"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
)

func TestNetworkCheck(t *testing.T) {
	t.Cleanup(func() { fetch.UseProxy() }) // after the variables below are restored
	for _, k := range []string{"https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("HTTPS_PROXY", "http://user:pw@127.0.0.1:3128")
	cfg := config.Default()
	if c := networkCheck(cfg, useProxy(cfg)); !c.ok || c.detail != "direct connections" {
		t.Errorf("HTTPS_PROXY is ignored without use_proxy: %+v", c)
	}
	cfg.UseProxy = true
	if c := networkCheck(cfg, useProxy(cfg)); !c.ok || c.detail != "through the proxy at http://127.0.0.1:3128 (use_proxy)" {
		t.Errorf("use_proxy: %+v", c)
	}
	t.Setenv("HTTPS_PROXY", "")
	if c := networkCheck(cfg, useProxy(cfg)); c.ok {
		t.Errorf("use_proxy without HTTPS_PROXY: %+v", c)
	}
}

func TestVersionOfADevelopmentCheckout(t *testing.T) {
	t.Setenv("REFETCH_PLUGIN_VERSION", "0.0.0")
	if c := versionCheck(); !c.ok {
		t.Errorf("REFETCH_BIN with a development checkout: %+v", c)
	}
}
