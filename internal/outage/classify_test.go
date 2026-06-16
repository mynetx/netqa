package outage

import (
	"testing"

	"github.com/mynetx/netqa/internal/model"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name       string
		state      State
		wantOutage bool
		wantClass  model.OutageClass
	}{
		{
			name:       "all good - no outage",
			state:      State{Awake: true, WifiOn: true, GatewayUp: true, InternetUp: true},
			wantOutage: false,
		},
		{
			name:       "confirmed ISP outage: awake, wifi on, gateway up, internet down",
			state:      State{Awake: true, WifiOn: true, GatewayUp: true, InternetUp: false},
			wantOutage: true,
			wantClass:  model.OutageISP,
		},
		{
			name:       "asleep is never an outage",
			state:      State{Awake: false, WifiOn: true, GatewayUp: true, InternetUp: false},
			wantOutage: true,
			wantClass:  model.OutageLocal,
		},
		{
			name:       "wifi off is local, not ISP",
			state:      State{Awake: true, WifiOn: false, GatewayUp: false, InternetUp: false},
			wantOutage: true,
			wantClass:  model.OutageLocal,
		},
		{
			name:       "gateway unreachable is local LAN, not ISP",
			state:      State{Awake: true, WifiOn: true, GatewayUp: false, InternetUp: false},
			wantOutage: true,
			wantClass:  model.OutageLocal,
		},
		{
			name:       "internet up while gateway down is impossible-ish but counts as up",
			state:      State{Awake: true, WifiOn: true, GatewayUp: false, InternetUp: true},
			wantOutage: false,
		},
		{
			name:       "VPN up but underlying link fine: no outage",
			state:      State{Awake: true, WifiOn: true, GatewayUp: true, InternetUp: true, VPN: true},
			wantOutage: false,
		},
		{
			name:       "VPN up and real link down: still ISP outage, flagged vpn",
			state:      State{Awake: true, WifiOn: true, GatewayUp: true, InternetUp: false, VPN: true},
			wantOutage: true,
			wantClass:  model.OutageISP,
		},
		{
			name:       "traceroute shows break past ISP -> upstream",
			state:      State{Awake: true, WifiOn: true, GatewayUp: true, InternetUp: false, PathBreaksPastISP: true},
			wantOutage: true,
			wantClass:  model.OutageUpstream,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOutage, gotClass := Classify(tt.state)
			if gotOutage != tt.wantOutage {
				t.Fatalf("outage = %v want %v", gotOutage, tt.wantOutage)
			}
			if gotOutage && gotClass != tt.wantClass {
				t.Fatalf("class = %q want %q", gotClass, tt.wantClass)
			}
		})
	}
}

func TestCountsAgainstISP(t *testing.T) {
	if CountsAgainstISP(model.OutageLocal) {
		t.Fatal("local must not count against ISP")
	}
	if !CountsAgainstISP(model.OutageISP) {
		t.Fatal("isp must count against ISP")
	}
	if !CountsAgainstISP(model.OutageUpstream) {
		t.Fatal("upstream must count against ISP")
	}
}
