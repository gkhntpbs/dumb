package main

import (
	"testing"

	"github.com/gkhntpbs/dumb/internal/server"
)

func TestParseRate(t *testing.T) {
	good := map[string]server.Rate{
		"": {}, "off": {PerSecond: -1}, "50,100": {PerSecond: 50, Burst: 100}, "0.5, 10": {PerSecond: 0.5, Burst: 10},
	}
	for in, want := range good {
		if got, err := parseRate(in); err != nil || got != want {
			t.Errorf("%q: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"50", "x,1", "0,10", "-1,10", "5,0", "5,10,1"} {
		if _, err := parseRate(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParsePrefixes(t *testing.T) {
	ps, err := parsePrefixes(" 10.0.0.0/8, 192.0.2.7 ,::1 ")
	if err != nil || len(ps) != 3 || ps[0].String() != "10.0.0.0/8" || ps[1].String() != "192.0.2.7/32" || ps[2].String() != "::1/128" {
		t.Fatalf("%v %v", ps, err)
	}
	if ps, err := parsePrefixes(""); err != nil || ps != nil {
		t.Fatalf("empty: %v %v", ps, err)
	}
	if _, err := parsePrefixes("10.0.0.0/33"); err == nil {
		t.Fatal("bad prefix accepted")
	}
}
