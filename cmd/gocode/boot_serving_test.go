package main

import (
	"net"
	"testing"
)

func TestLoopbackURLDialsWildcardBindsOnLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "127.0.0.1:0"} {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_, port, _ := net.SplitHostPort(listener.Addr().String())
		got := loopbackURL(listener)
		listener.Close()
		if got != "http://127.0.0.1:"+port {
			t.Errorf("loopbackURL(%s) = %q", addr, got)
		}
	}
}
