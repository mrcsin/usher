package xray

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	statscmd "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"

	"github.com/mrcsin/usher/internal/status"
)

type fakeStats struct {
	statscmd.StatsServiceClient
	queryStats  func(*statscmd.QueryStatsRequest) (*statscmd.QueryStatsResponse, error)
	onlineUsers func() (*statscmd.GetAllOnlineUsersResponse, error)

	calls int
}

func (f *fakeStats) QueryStats(_ context.Context, in *statscmd.QueryStatsRequest,
	_ ...grpc.CallOption) (*statscmd.QueryStatsResponse, error) {
	f.calls++
	return f.queryStats(in)
}

func (f *fakeStats) GetAllOnlineUsers(_ context.Context, _ *statscmd.GetAllOnlineUsersRequest,
	_ ...grpc.CallOption) (*statscmd.GetAllOnlineUsersResponse, error) {
	f.calls++
	return f.onlineUsers()
}

func TestUserStats(t *testing.T) {
	tests := []struct {
		name     string
		users    []string
		counters map[string]int64
		online   []string
		want     map[string]status.UserStats
	}{
		{
			name:  "counters present",
			users: []string{"alice"},
			counters: map[string]int64{
				"user>>>in/alice>>>traffic>>>uplink":   10,
				"user>>>in/alice>>>traffic>>>downlink": 20,
			},
			want: map[string]status.UserStats{"alice": {Uplink: 10, Downlink: 20}},
		},
		{
			name:  "user without counters gets zeros",
			users: []string{"alice"},
			want:  map[string]status.UserStats{"alice": {}},
		},
		{
			name:   "online user",
			users:  []string{"alice", "bob"},
			online: []string{"user>>>in/alice>>>online"},
			want:   map[string]status.UserStats{"alice": {Online: true}, "bob": {}},
		},
		{
			name:  "counter of another tag with a shared prefix is ignored",
			users: []string{"alice"},
			counters: map[string]int64{
				"user>>>in2/alice>>>traffic>>>uplink": 5,
				"user>>>in/alice2>>>traffic>>>uplink": 6,
			},
			online: []string{"user>>>in2/alice>>>online"},
			want:   map[string]status.UserStats{"alice": {}},
		},
		{
			name:  "counter of a removed user of the same tag is ignored",
			users: []string{"alice"},
			counters: map[string]int64{
				"user>>>in/alice>>>traffic>>>uplink": 1,
				"user>>>in/gone>>>traffic>>>uplink":  7,
			},
			online: []string{"user>>>in/gone>>>online"},
			want:   map[string]status.UserStats{"alice": {Uplink: 1}},
		},
		{
			name:  "negative counter reads 0",
			users: []string{"alice"},
			counters: map[string]int64{
				"user>>>in/alice>>>traffic>>>uplink":   -1,
				"user>>>in/alice>>>traffic>>>downlink": 2,
			},
			want: map[string]status.UserStats{"alice": {Downlink: 2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := userStats(tt.users, "in", tt.counters, tt.online)
			if !maps.Equal(got, tt.want) {
				t.Errorf("userStats = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionStats(t *testing.T) {
	var pattern string
	fake := &fakeStats{
		queryStats: func(in *statscmd.QueryStatsRequest) (*statscmd.QueryStatsResponse, error) {
			pattern = in.GetPattern()
			return &statscmd.QueryStatsResponse{Stat: []*statscmd.Stat{
				{Name: "user>>>in/alice>>>traffic>>>uplink", Value: 3},
				{Name: "user>>>in/alice>>>traffic>>>downlink", Value: 4},
			}}, nil
		},
		onlineUsers: func() (*statscmd.GetAllOnlineUsersResponse, error) {
			return &statscmd.GetAllOnlineUsersResponse{Users: []string{"user>>>in/alice>>>online"}}, nil
		},
	}
	s := &Session{stats: fake}
	got, err := s.Stats(t.Context(), newState(), "in", []string{"alice", "bob"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]status.UserStats{"alice": {Uplink: 3, Downlink: 4, Online: true}, "bob": {}}
	if !maps.Equal(got, want) {
		t.Errorf("Stats = %v, want %v", got, want)
	}
	if pattern != "user>>>in/" {
		t.Errorf("QueryStats pattern = %q, want %q", pattern, "user>>>in/")
	}

	got, err = s.Stats(t.Context(), newState(), "in2", []string{"alice"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]status.UserStats{"alice": {}}; !maps.Equal(got, want) {
		t.Errorf("Stats of in2 = %v, want %v", got, want)
	}
	if fake.calls != 3 {
		t.Errorf("API calls for two inbounds = %d, want 3: GetAllOnlineUsers runs once per session", fake.calls)
	}
}

func TestSessionStatsFailures(t *testing.T) {
	apiErr := errors.New("api down")
	okQuery := func(*statscmd.QueryStatsRequest) (*statscmd.QueryStatsResponse, error) {
		return &statscmd.QueryStatsResponse{}, nil
	}
	okOnline := func() (*statscmd.GetAllOnlineUsersResponse, error) {
		return &statscmd.GetAllOnlineUsersResponse{}, nil
	}
	tests := []struct {
		name      string
		missing   []string
		fake      *fakeStats
		wantErr   string
		wantCalls int
	}{
		{
			name:      "switches missing",
			missing:   []string{"stats", "StatsService in api.services"},
			fake:      &fakeStats{queryStats: okQuery, onlineUsers: okOnline},
			wantErr:   "stats are off in the xray config: stats, StatsService in api.services",
			wantCalls: 0,
		},
		{
			name: "QueryStats fails",
			fake: &fakeStats{queryStats: func(*statscmd.QueryStatsRequest) (*statscmd.QueryStatsResponse, error) {
				return nil, apiErr
			}, onlineUsers: okOnline},
			wantErr:   "querying stats: api down",
			wantCalls: 1,
		},
		{
			name: "GetAllOnlineUsers fails",
			fake: &fakeStats{queryStats: okQuery, onlineUsers: func() (*statscmd.GetAllOnlineUsersResponse, error) {
				return nil, apiErr
			}},
			wantErr:   "listing online users: api down",
			wantCalls: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{stats: tt.fake, statsMissing: tt.missing}
			got, err := s.Stats(t.Context(), newState(), "in", []string{"alice"})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Stats error = %v, want %q", err, tt.wantErr)
			}
			if tt.wantCalls > 0 && !errors.Is(err, apiErr) {
				t.Errorf("Stats error = %v, want it to wrap %v", err, apiErr)
			}
			if got != nil {
				t.Errorf("Stats = %v, want nil", got)
			}
			if tt.fake.calls != tt.wantCalls {
				t.Errorf("API calls = %d, want %d", tt.fake.calls, tt.wantCalls)
			}
		})
	}
}
