package xray

import (
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/mrcsin/usher/internal/clients"
	"github.com/mrcsin/usher/internal/state"
)

var linkEntry = state.XrayEntry{ID: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}}

func linkParams(sid string) url.Values {
	return url.Values{
		"type":     {"tcp"},
		"security": {"reality"},
		"sni":      {"example.com"},
		"fp":       {"chrome"},
		"pbk":      {"AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-AbCdE"},
		"sid":      {sid},
	}
}

func TestVLESSLinkGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/vless.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := vlessLink(linkEntry, "alice", netip.MustParseAddr("203.0.113.7"), 443, linkParams("ab")) + "\n"
	if got != string(want) {
		t.Errorf("link differs from testdata/vless.txt:\n%s", got)
	}
}

func TestVLESSLinkParts(t *testing.T) {
	tests := []struct {
		name string
		host string
		sid  string
		user string
		want []string
	}{
		{name: "empty sid", host: "203.0.113.7", sid: "", user: "alice", want: []string{"&sid=&", "#alice"}},
		{name: "user name escaped", host: "203.0.113.7", sid: "ab", user: "a b#c/d", want: []string{"#a%20b%23c%2Fd"}},
		{name: "ipv6 host in brackets", host: "2001:db8::1", sid: "ab", user: "alice", want: []string{"@[2001:db8::1]:443?"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			link := vlessLink(linkEntry, tt.user, netip.MustParseAddr(tt.host), 443, linkParams(tt.sid))
			for _, part := range tt.want {
				if !strings.Contains(link, part) {
					t.Errorf("link %q does not contain %q", link, part)
				}
			}
			parsed, err := url.Parse(link)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Query().Get("sid") != tt.sid || parsed.Fragment != tt.user {
				t.Errorf("sid %q fragment %q, want %q and %q", parsed.Query().Get("sid"), parsed.Fragment, tt.sid, tt.user)
			}
		})
	}
}

func TestPrepareRendersLinkFiles(t *testing.T) {
	env := newSessionEnv(t, twoInbounds(t))
	s := env.open()
	files, _, err := s.Prepare(env.st, "vless", []string{"alice", "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	for _, user := range []string{"alice", "bob"} {
		content := string(files[clients.Path(user, "vless", Suffix)])
		wantPrefix := "vless://" + env.st.Xray[state.EntryName("vless", user)].ID.String() + "@203.0.113.7:443?"
		if !strings.HasPrefix(content, wantPrefix) || !strings.HasSuffix(content, "#"+user+"\n") {
			t.Errorf("%s link = %q", user, content)
		}
	}
}
