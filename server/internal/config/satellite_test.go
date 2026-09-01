package config

import (
	"math"
	"testing"
)

func TestSatelliteConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  SatelliteConfig
		ok   bool
	}{
		{"defaults", SatelliteConfig{Addr: "localhost:16622", Rows: 4, Cols: 8, BitmapSize: 72}, true},
		{"zero bitmap selects default", SatelliteConfig{BitmapSize: 0}, true},
		{"bitmap below companion floor", SatelliteConfig{BitmapSize: 3}, false},
		{"bitmap at floor", SatelliteConfig{BitmapSize: 5}, true},
		{"bitmap at the cap", SatelliteConfig{BitmapSize: 256}, true},
		{"bitmap above the cap", SatelliteConfig{BitmapSize: 257}, false},
		{"implausible bitmap", SatelliteConfig{BitmapSize: 7200}, false},
		{"negative bitmap", SatelliteConfig{BitmapSize: -1}, false},
		{"negative rows", SatelliteConfig{Rows: -1}, false},
		{"a wall of Stream Deck XLs", SatelliteConfig{Rows: 4, Cols: 32}, true},
		{"grid at the key cap", SatelliteConfig{Rows: 16, Cols: 16}, true},
		{"grid one key past the cap", SatelliteConfig{Rows: 16, Cols: 17}, false},
		{"a grid no operator could read", SatelliteConfig{Rows: 64, Cols: 64}, false},
		{"implausible rows", SatelliteConfig{Rows: 1000, Cols: 8}, false},
		{"implausible cols", SatelliteConfig{Rows: 4, Cols: 100000}, false},
		// Their product wraps negative, which the ">" check alone would accept.
		{"dimensions that overflow their product", SatelliteConfig{Rows: math.MaxInt/2 + 1, Cols: 2}, false},
		{"disabled skips checks", SatelliteConfig{Addr: "off", BitmapSize: 3}, true},
		{"device_id with space", SatelliteConfig{DeviceID: "cue booth"}, false},
		{"device_id with equals", SatelliteConfig{DeviceID: "cue=booth"}, false},
		{"empty device_id is ok (defaulted)", SatelliteConfig{DeviceID: ""}, true},
		{"simple device_id", SatelliteConfig{DeviceID: "cuebooth-1"}, true},
	}
	for _, tc := range cases {
		err := tc.cfg.validate()
		if tc.ok && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected an error, got nil", tc.name)
		}
	}
}

func TestSatelliteConfigDisabled(t *testing.T) {
	for _, addr := range []string{"off", "OFF", "disabled", "none", " off "} {
		if !(SatelliteConfig{Addr: addr}).Disabled() {
			t.Errorf("addr %q should be disabled", addr)
		}
	}
	if (SatelliteConfig{Addr: "localhost:16622"}).Disabled() {
		t.Error("a real addr should not be disabled")
	}
}
