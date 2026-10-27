package xray

import (
	"context"
	"fmt"
	"strings"

	statscmd "github.com/xtls/xray-core/app/stats/command"

	"github.com/mrcsin/usher/internal/state"
	"github.com/mrcsin/usher/internal/status"
)

// Stats reads the traffic counters and the online users of the inbound and returns an entry for
// every requested user. A user without counters has not connected since Xray started and gets
// zeros.
func (s *Session) Stats(ctx context.Context, _ *state.State, name string, users []string) (map[string]status.UserStats, error) {
	if len(s.statsMissing) > 0 {
		return nil, fmt.Errorf("stats are off in the xray config: %s", strings.Join(s.statsMissing, ", "))
	}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	// QueryStats matches by substring; userStats keeps only exact names of requested users.
	resp, err := s.stats.QueryStats(callCtx, &statscmd.QueryStatsRequest{Pattern: userStatPrefix + state.EntryName(name, "")})
	if err != nil {
		return nil, fmt.Errorf("querying stats: %w", err)
	}
	counters := make(map[string]int64, len(resp.GetStat()))
	for _, stat := range resp.GetStat() {
		counters[stat.GetName()] = stat.GetValue()
	}
	// The online list covers every inbound, so one call serves the whole session.
	if !s.onlineRead {
		resp, err := s.stats.GetAllOnlineUsers(callCtx, &statscmd.GetAllOnlineUsersRequest{})
		if err != nil {
			return nil, fmt.Errorf("listing online users: %w", err)
		}
		s.online, s.onlineRead = resp.GetUsers(), true
	}
	return userStats(users, name, counters, s.online), nil
}

// userStatPrefix starts the name of every per-user stat that Xray keeps.
const userStatPrefix = "user>>>"

// userStatName returns the name Xray gives the stat of a user on the inbound, such as
// "user>>>vless/phone>>>traffic>>>uplink".
func userStatName(tag, user, stat string) string {
	return userStatPrefix + state.EntryName(tag, user) + ">>>" + stat
}

// userStats looks up each requested user by the exact stat names Xray builds from its email.
func userStats(users []string, tag string, counters map[string]int64, online []string) map[string]status.UserStats {
	onlineNames := make(map[string]bool, len(online))
	for _, n := range online {
		onlineNames[n] = true
	}
	out := make(map[string]status.UserStats, len(users))
	for _, user := range users {
		out[user] = status.UserStats{
			Uplink:   uint64(max(counters[userStatName(tag, user, "traffic>>>uplink")], 0)),
			Downlink: uint64(max(counters[userStatName(tag, user, "traffic>>>downlink")], 0)),
			Online:   onlineNames[userStatName(tag, user, "online")],
		}
	}
	return out
}
