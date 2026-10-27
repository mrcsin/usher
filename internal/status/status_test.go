package status

import (
	"slices"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	tests := []struct {
		name       string
		interfaces []Interface
		want       string
	}{
		{
			name: "up interface with two users",
			interfaces: []Interface{{
				Name: "awg0",
				Up:   true,
				Users: map[string]UserStats{
					"bob":   {Uplink: 10, Downlink: 20},
					"alice": {Uplink: 1234, Downlink: 5678, Online: true},
				},
			}},
			want: `# HELP usher_interface_up 1 when the last pass applied the interface.
# TYPE usher_interface_up gauge
usher_interface_up{interface="awg0"} 1
# HELP usher_user_online 1 when the user is connected now.
# TYPE usher_user_online gauge
usher_user_online{interface="awg0",user="alice"} 1
usher_user_online{interface="awg0",user="bob"} 0
# HELP usher_user_uplink_bytes_total Bytes received from the user.
# TYPE usher_user_uplink_bytes_total counter
usher_user_uplink_bytes_total{interface="awg0",user="alice"} 1234
usher_user_uplink_bytes_total{interface="awg0",user="bob"} 10
# HELP usher_user_downlink_bytes_total Bytes sent to the user.
# TYPE usher_user_downlink_bytes_total counter
usher_user_downlink_bytes_total{interface="awg0",user="alice"} 5678
usher_user_downlink_bytes_total{interface="awg0",user="bob"} 20
`,
		},
		{
			name:       "down interface",
			interfaces: []Interface{{Name: "awg0"}},
			want: `# HELP usher_interface_up 1 when the last pass applied the interface.
# TYPE usher_interface_up gauge
usher_interface_up{interface="awg0"} 0
`,
		},
		{
			name:       "up interface with nil users",
			interfaces: []Interface{{Name: "vless-tcp", Up: true}},
			want: `# HELP usher_interface_up 1 when the last pass applied the interface.
# TYPE usher_interface_up gauge
usher_interface_up{interface="vless-tcp"} 1
`,
		},
		{
			name: "interfaces sorted by name",
			interfaces: []Interface{
				{Name: "vless-tcp", Up: true, Users: map[string]UserStats{"alice": {Uplink: 1, Downlink: 2}}},
				{Name: "awg0", Users: map[string]UserStats{"alice": {Uplink: 3, Downlink: 4, Online: true}}},
			},
			want: `# HELP usher_interface_up 1 when the last pass applied the interface.
# TYPE usher_interface_up gauge
usher_interface_up{interface="awg0"} 0
usher_interface_up{interface="vless-tcp"} 1
# HELP usher_user_online 1 when the user is connected now.
# TYPE usher_user_online gauge
usher_user_online{interface="awg0",user="alice"} 1
usher_user_online{interface="vless-tcp",user="alice"} 0
# HELP usher_user_uplink_bytes_total Bytes received from the user.
# TYPE usher_user_uplink_bytes_total counter
usher_user_uplink_bytes_total{interface="awg0",user="alice"} 3
usher_user_uplink_bytes_total{interface="vless-tcp",user="alice"} 1
# HELP usher_user_downlink_bytes_total Bytes sent to the user.
# TYPE usher_user_downlink_bytes_total counter
usher_user_downlink_bytes_total{interface="awg0",user="alice"} 4
usher_user_downlink_bytes_total{interface="vless-tcp",user="alice"} 2
`,
		},
		{
			name: "no interfaces",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(Render(tt.interfaces))
			if got != tt.want {
				t.Errorf("Render() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestRenderEscapesLabels(t *testing.T) {
	tests := []struct {
		name string
		user string
		want string
	}{
		{name: "quote", user: `a"b`, want: `user="a\"b"`},
		{name: "backslash", user: `a\b`, want: `user="a\\b"`},
		{name: "newline", user: "a\nb", want: `user="a\nb"`},
		{name: "all three", user: "\\\"\n", want: `user="\\\"\n"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(Render([]Interface{{
				Name:  "awg0",
				Up:    true,
				Users: map[string]UserStats{tt.user: {}},
			}}))
			want := `usher_user_online{interface="awg0",` + tt.want + "} 0\n"
			if !slices.Contains(strings.SplitAfter(got, "\n"), want) {
				t.Errorf("Render() =\n%s\nwant a line %q", got, want)
			}
		})
	}
}
