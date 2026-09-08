package vehicles

import (
	"testing"
)

func TestNormalizeVIN(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "already normalized", raw: "1HGCM82633A004352", want: "1HGCM82633A004352"},
		{name: "lowercase accepted and uppercased", raw: "1hgcm82633a004352", want: "1HGCM82633A004352"},
		{name: "spaces and hyphens stripped", raw: "1HGC-M826 33A-004352", want: "1HGCM82633A004352"},
		{name: "sixteen characters rejected", raw: "1HGCM82633A00435", wantErr: true},
		{name: "eighteen characters rejected", raw: "1HGCM82633A0043522", wantErr: true},
		{name: "empty rejected", raw: "", wantErr: true},
		{name: "only separators rejected", raw: " --  -- ", wantErr: true},
		{name: "contains I rejected", raw: "1HGCM826I3A004352", wantErr: true},
		{name: "contains O rejected", raw: "1HGCM826O3A004352", wantErr: true},
		{name: "contains Q rejected", raw: "1HGCM826Q3A004352", wantErr: true},
		{name: "leading I rejected", raw: "IHGCM82633A004352", wantErr: true},
		{name: "invalid symbol rejected", raw: "1HGCM826*3A004352", wantErr: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeVIN(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeVIN(%q) = %q, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeVIN(%q) returned error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeVIN(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestValidateVIN(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "valid honda vector", raw: "1HGCM82633A004352", want: "1HGCM82633A004352"},
		{name: "valid X check digit vector", raw: "1M8GDM9AXKP042788", want: "1M8GDM9AXKP042788"},
		{name: "lowercase vector accepted after normalize", raw: "1m8gdm9axkp042788", want: "1M8GDM9AXKP042788"},
		{name: "bad checksum", raw: "1HGCM82633A004353", wantErr: true},
		{name: "bad checksum lowercase", raw: "1hgcm82633a004353", wantErr: true},
		{name: "sixteen characters", raw: "1HGCM82633A00435", wantErr: true},
		{name: "contains forbidden I", raw: "1HGCM826I3A004352", wantErr: true},
		{name: "space padded to seventeen is valid", raw: "1HGCM826-33A 004352", want: "1HGCM82633A004352"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateVIN(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateVIN(%q) = %q, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateVIN(%q) returned error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("ValidateVIN(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestVINCheckDigit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		vin  string
		want byte
	}{
		{vin: "1HGCM82633A004352", want: '3'},
		{vin: "1M8GDM9AXKP042788", want: 'X'},
	}
	for _, tc := range cases {
		got, err := vinCheckDigit(tc.vin)
		if err != nil {
			t.Fatalf("vinCheckDigit(%q) returned error: %v", tc.vin, err)
		}
		if got != tc.want {
			t.Fatalf("vinCheckDigit(%q) = %q, want %q", tc.vin, got, tc.want)
		}
	}
	if _, err := vinCheckDigit("short"); err == nil {
		t.Fatal("vinCheckDigit(short) = nil error, want error")
	}
}
