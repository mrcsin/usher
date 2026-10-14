// Package xray reads the operator's Xray server config and keeps the users of its inbounds in step
// with usher.yml.
package xray

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/infra/conf"
	xjson "github.com/xtls/xray-core/infra/conf/json"

	"github.com/mrcsin/usher/internal/config"
)

// Inbound is one managed inbound of the Xray server config. Raw is the inbound object with the
// config aliases normalized to their canonical names. A non-nil Err means the inbound failed
// validation and must not be used.
type Inbound struct {
	Tag      string
	Protocol string // lower case: vless, hysteria, vmess or trojan
	Port     int
	Network  string // raw, ws, grpc, httpupgrade, xhttp or hysteria
	Security string // none, tls or reality
	// ServerName is the name the client sends: x-usher.sni, else the first usable Reality
	// serverNames entry or the tlsSettings serverName. It is empty when security is none.
	ServerName string
	Hints      Hints
	Raw        map[string]any
	Err        error
}

// Hints is the x-usher block of an inbound with the defaults that do not depend on usher's
// environment applied. An empty Address means USHER_HOST.
type Hints struct {
	Address     string
	Port        int
	Fingerprint string
	Trust       string // "ca" or "pin"
	PinSHA256   string // lower case hex, empty when the pin comes from the inline certificate
	HopPorts    string // "from-to", Hysteria2 only
}

const (
	// TrustCA makes the client verify the server certificate against its CA roots.
	TrustCA = "ca"
	// TrustPin makes the client also pin the leaf certificate.
	TrustPin = "pin"

	defaultFingerprint = "chrome"
)

var (
	// realityFingerprints are the uTLS fingerprints that carry X25519MLKEM768, which the Reality
	// server requires.
	realityFingerprints = []string{"chrome", "firefox", "safari"}

	networkAliases = map[string]string{"tcp": "raw", "splithttp": "xhttp", "websocket": "ws"}
	networks       = map[string]bool{
		"raw": true, "ws": true, "grpc": true, "httpupgrade": true, "xhttp": true, "hysteria": true,
	}
	hopPortsPattern = regexp.MustCompile(`^([0-9]{1,5})-([0-9]{1,5})$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// discardLog drops Xray's global log output. Build prints deprecation warnings, and usher runs no
// Xray instance whose log they belong to.
type discardLog struct{}

func (discardLog) Handle(log.Message) {}

var silenceOnce sync.Once

func silenceXrayLog() {
	silenceOnce.Do(func() { log.RegisterHandler(discardLog{}) })
}

// ReadConfig reads the Xray server config at path and returns its managed inbounds in file order.
// Inbounds of other protocols and inbounds without a tag are skipped. An inbound that fails
// validation is returned with Err set, so the caller can keep its files. An error means the file
// as a whole is unusable.
func ReadConfig(path string) ([]Inbound, error) {
	silenceXrayLog()
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading xray config: %w", err)
	}
	defer f.Close()

	var file struct {
		Inbounds []json.RawMessage `json:"inbounds"`
	}
	if err := json.NewDecoder(&xjson.Reader{Reader: f}).Decode(&file); err != nil {
		return nil, fmt.Errorf("parsing xray config %s: %w", path, err)
	}

	var inbounds []Inbound
	seen := map[string]bool{}
	for i, data := range file.Inbounds {
		raw, err := decodeObject(data)
		if err != nil {
			return nil, fmt.Errorf("parsing xray config %s: inbounds[%d]: %w", path, i, err)
		}
		tag, _ := raw["tag"].(string)
		protocol, _ := raw["protocol"].(string)
		protocol = strings.ToLower(protocol)
		if _, managed := protocols[protocol]; tag == "" || !managed {
			continue
		}
		in := Inbound{Tag: tag, Protocol: protocol, Raw: raw}
		switch {
		case seen[tag]:
			in.Err = fmt.Errorf("inbound %q: tag: defined twice", tag)
		case !config.ValidInterfaceName(tag):
			in.Err = fmt.Errorf("inbound %q: tag: not a valid interface name", tag)
		default:
			in.Err = in.validate()
		}
		seen[tag] = true
		inbounds = append(inbounds, in)
	}
	return inbounds, nil
}

func decodeObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, errors.New("want an object")
	}
	return obj, nil
}

// validate normalizes in.Raw, checks it against what the backend supports and fills the derived
// fields. Every error names the field and never quotes a value.
func (in *Inbound) validate() error {
	fail := func(field, reason string) error {
		return fmt.Errorf("inbound %q: %s: %s", in.Tag, field, reason)
	}

	port, err := in.singlePort()
	if err != nil {
		return fail("port", err.Error())
	}
	in.Port = port
	if listen, ok := in.Raw["listen"].(string); ok && (strings.HasPrefix(listen, "/") || strings.HasPrefix(listen, "@")) {
		return fail("listen", "unix socket is not supported")
	}

	settings, err := in.settings()
	if err != nil {
		return fail("settings", err.Error())
	}
	if err := in.checkUsersEmpty(settings); err != nil {
		return fail("settings.clients", err.Error())
	}
	if in.Protocol == "vless" && lowerString(settings["decryption"]) != "none" {
		return fail("settings.decryption", `want "none"`)
	}

	stream, err := in.normalizeStream()
	if err != nil {
		return fail("streamSettings", err.Error())
	}
	if field, reason := in.checkTransport(stream); field != "" {
		return fail(field, reason)
	}
	if field, reason := in.checkHysteria(stream); field != "" {
		return fail(field, reason)
	}
	if field, reason := in.checkMasks(stream); field != "" {
		return fail(field, reason)
	}
	if field, reason := in.checkXHTTP(stream); field != "" {
		return fail(field, reason)
	}
	if in.Security == "reality" {
		if _, _, field, reason := realityKeys(object(stream["realitySettings"])); field != "" {
			return fail("streamSettings.realitySettings."+field, reason)
		}
	}
	if field, reason := in.readHints(stream); field != "" {
		return fail(field, reason)
	}

	// The decode catches field type errors that the checks above do not look at. Xray's decoders
	// can quote the value they reject, so the error names the Go field of a type mismatch at most.
	encoded, err := json.Marshal(in.Raw)
	if err != nil {
		return fail("inbound", "cannot be encoded")
	}
	var detour conf.InboundDetourConfig
	if err := json.Unmarshal(encoded, &detour); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return fail("inbound", "does not decode as an Xray inbound: wrong type at "+typeErr.Field)
		}
		return fail("inbound", "does not decode as an Xray inbound")
	}
	return nil
}

func (in *Inbound) singlePort() (int, error) {
	number, ok := in.Raw["port"].(json.Number)
	if !ok {
		return 0, errors.New("want a single integer")
	}
	port, err := strconv.Atoi(number.String())
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("want a single integer from 1 to 65535")
	}
	return port, nil
}

func (in *Inbound) settings() (map[string]any, error) {
	v, present := in.Raw["settings"]
	if !present {
		settings := map[string]any{}
		in.Raw["settings"] = settings
		return settings, nil
	}
	settings, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("want an object")
	}
	return settings, nil
}

// checkUsersEmpty folds the Hysteria alias users into clients and requires the list to be empty,
// because usher owns every user of the inbound.
func (in *Inbound) checkUsersEmpty(settings map[string]any) error {
	if in.Protocol == "hysteria" {
		if users, ok := settings["users"]; ok {
			if clients, both := settings["clients"]; both && !isEmptyList(clients) {
				return errors.New("users and clients are both set")
			}
			settings["clients"] = users
			delete(settings, "users")
		}
	}
	if clients, ok := settings["clients"]; ok && !isEmptyList(clients) {
		return errors.New("must be empty, usher owns the users")
	}
	return nil
}

func isEmptyList(v any) bool {
	list, ok := v.([]any)
	return v == nil || (ok && len(list) == 0)
}

// normalizeStream rewrites the aliases of streamSettings to their canonical names and fills in
// Network and Security. It returns the streamSettings object, a detached empty one when the inbound
// has none.
func (in *Inbound) normalizeStream() (map[string]any, error) {
	v, present := in.Raw["streamSettings"]
	stream, ok := v.(map[string]any)
	if present && !ok {
		return nil, errors.New("want an object")
	}
	if stream == nil {
		// Not stored in Raw: an inbound without streamSettings must build the way Xray builds
		// the original file.
		stream = map[string]any{}
	}

	// Xray lets method win over network.
	if method, ok := stream["method"]; ok {
		stream["network"] = method
		delete(stream, "method")
	}
	network := "raw"
	if v, ok := stream["network"]; ok {
		s, isString := v.(string)
		if !isString {
			return nil, errors.New("network: want a string")
		}
		network = strings.ToLower(s)
		if canonical, ok := networkAliases[network]; ok {
			network = canonical
		}
	}
	stream["network"] = network
	in.Network = network

	for alias, canonical := range map[string]string{
		"splithttpSettings": "xhttpSettings",
		"tcpSettings":       "rawSettings",
	} {
		v, ok := stream[alias]
		if !ok {
			continue
		}
		if _, clash := stream[canonical]; clash {
			return nil, fmt.Errorf("%s: conflicts with %s", alias, canonical)
		}
		stream[canonical] = v
		delete(stream, alias)
	}

	security := "none"
	if v, ok := stream["security"]; ok {
		s, isString := v.(string)
		if !isString {
			return nil, errors.New("security: want a string")
		}
		if s != "" {
			security = strings.ToLower(s)
		}
	}
	stream["security"] = security
	in.Security = security
	return stream, nil
}

func (in *Inbound) checkTransport(stream map[string]any) (field, reason string) {
	if !networks[in.Network] {
		return "streamSettings.network", "unsupported transport"
	}
	switch in.Security {
	case "none", "tls", "reality":
	default:
		return "streamSettings.security", "want none, tls or reality"
	}
	if (in.Protocol == "hysteria") != (in.Network == "hysteria") {
		return "streamSettings.network", "hysteria transport and hysteria protocol go together"
	}
	if in.Network == "raw" {
		header := object(object(stream["rawSettings"])["header"])
		if t := lowerString(header["type"]); t != "" && t != "none" {
			return "streamSettings.rawSettings.header.type", `want "none"`
		}
	}
	return "", ""
}

func (in *Inbound) checkHysteria(stream map[string]any) (field, reason string) {
	if in.Protocol != "hysteria" {
		return "", ""
	}
	if in.Security != "tls" {
		return "streamSettings.security", `hysteria needs "tls"`
	}
	if !isVersion2(object(in.Raw["settings"])["version"]) {
		return "settings.version", "want 2"
	}
	hysteria := object(stream["hysteriaSettings"])
	if !isVersion2(hysteria["version"]) {
		return "streamSettings.hysteriaSettings.version", "want 2"
	}
	if auth, _ := hysteria["auth"].(string); auth != "" {
		return "streamSettings.hysteriaSettings.auth", "must be empty, usher gives each user an auth"
	}
	return "", ""
}

func isVersion2(v any) bool {
	n, ok := v.(json.Number)
	return ok && n.String() == "2"
}

// checkMasks allows only salamander without packetSize in the udp list, and quicParams.
func (in *Inbound) checkMasks(stream map[string]any) (field, reason string) {
	mask, present := stream["finalmask"]
	if !present {
		return "", ""
	}
	finalmask, ok := mask.(map[string]any)
	if !ok {
		return "streamSettings.finalmask", "want an object"
	}
	if tcp, ok := finalmask["tcp"]; ok && !isEmptyList(tcp) {
		return "streamSettings.finalmask.tcp", "unsupported mask"
	}
	udp, ok := finalmask["udp"].([]any)
	if !ok && finalmask["udp"] != nil {
		return "streamSettings.finalmask.udp", "want a list"
	}
	for _, item := range udp {
		entry := object(item)
		if lowerString(entry["type"]) != "salamander" {
			return "streamSettings.finalmask.udp", "only salamander is supported"
		}
		if _, ok := object(entry["settings"])["packetSize"]; ok {
			return "streamSettings.finalmask.udp.settings.packetSize", "unsupported with salamander"
		}
	}
	if in.Protocol != "hysteria" && len(udp) > 0 {
		return "streamSettings.finalmask.udp", "salamander is supported on hysteria only"
	}
	return "", ""
}

func (in *Inbound) checkXHTTP(stream map[string]any) (field, reason string) {
	if in.Network != "xhttp" {
		return "", ""
	}
	xhttp := object(stream["xhttpSettings"])
	if _, ok := xhttp["downloadSettings"]; ok {
		return "streamSettings.xhttpSettings.downloadSettings", "unsupported"
	}
	extra := xhttp["extra"]
	if s, ok := extra.(string); ok && s != "" {
		return "streamSettings.xhttpSettings.extra", "want an object"
	}
	if _, ok := object(extra)["downloadSettings"]; ok {
		return "streamSettings.xhttpSettings.extra.downloadSettings", "unsupported"
	}
	return "", ""
}

// xUsher mirrors the x-usher block. Pointers tell an absent field from a zero one.
type xUsher struct {
	Address     string `json:"address"`
	Port        *int   `json:"port"`
	SNI         string `json:"sni"`
	Fingerprint string `json:"fingerprint"`
	Trust       string `json:"trust"`
	PinSHA256   string `json:"pin_sha256"`
	HopPorts    string `json:"hop_ports"`
}

// readHints decodes x-usher, checks it against the inbound and fills Hints and ServerName.
func (in *Inbound) readHints(stream map[string]any) (field, reason string) {
	var hint xUsher
	if block, ok := in.Raw["x-usher"]; ok {
		encoded, err := json.Marshal(block)
		if err != nil {
			return "x-usher", "cannot be encoded"
		}
		dec := json.NewDecoder(bytes.NewReader(encoded))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&hint); err != nil {
			return "x-usher", err.Error()
		}
	}

	port := in.Port
	if hint.Port != nil {
		if *hint.Port < 1 || *hint.Port > 65535 {
			return "x-usher.port", "want 1 to 65535"
		}
		port = *hint.Port
	}

	trust := hint.Trust
	switch trust {
	case "":
		trust = TrustCA
	case TrustCA, TrustPin:
	default:
		return "x-usher.trust", `want "ca" or "pin"`
	}
	if trust == TrustPin && in.Security != "tls" {
		return "x-usher.trust", `"pin" needs security "tls"`
	}
	if hint.PinSHA256 != "" {
		if trust != TrustPin {
			return "x-usher.pin_sha256", `valid only with trust "pin"`
		}
		if !sha256Pattern.MatchString(hint.PinSHA256) {
			return "x-usher.pin_sha256", "want 64 hex characters"
		}
	}
	if trust == TrustPin && hint.PinSHA256 == "" && !hasInlineCertificate(stream) {
		return "x-usher.pin_sha256", `trust "pin" needs it or an inline certificate`
	}

	fingerprint := hint.Fingerprint
	if fingerprint == "" {
		fingerprint = defaultFingerprint
	}
	if in.Security == "reality" && !slices.Contains(realityFingerprints, fingerprint) {
		return "x-usher.fingerprint", "reality supports chrome, firefox and safari only"
	}

	if hint.HopPorts != "" {
		if in.Protocol != "hysteria" {
			return "x-usher.hop_ports", "valid on hysteria only"
		}
		if !validHopPorts(hint.HopPorts) {
			return "x-usher.hop_ports", `want "from-to" with 1 <= from < to <= 65535`
		}
	}

	serverName, field, reason := in.serverName(stream, hint.SNI)
	if field != "" {
		return field, reason
	}
	in.ServerName = serverName
	in.Hints = Hints{
		Address:     hint.Address,
		Port:        port,
		Fingerprint: fingerprint,
		Trust:       trust,
		PinSHA256:   strings.ToLower(hint.PinSHA256),
		HopPorts:    hint.HopPorts,
	}
	return "", ""
}

// serverName resolves the name the client sends. An explicit x-usher.sni wins over the server
// config on TLS. Reality looks the name up in serverNames by exact match, so there the override
// must be one of the entries; it is how an operator picks one of several names.
func (in *Inbound) serverName(stream map[string]any, override string) (name, field, reason string) {
	switch in.Security {
	case "tls":
		name = override
		if name == "" {
			name, _ = object(stream["tlsSettings"])["serverName"].(string)
		}
		if name == "" {
			return "", "x-usher.sni", "tls needs it or tlsSettings.serverName"
		}
	case "reality":
		names, _ := object(stream["realitySettings"])["serverNames"].([]any)
		if override != "" {
			if !slices.Contains(names, any(override)) || strings.Contains(override, "*") {
				return "", "x-usher.sni", "reality needs one of its serverNames entries, without a wildcard"
			}
			return override, "", ""
		}
		for _, v := range names {
			if s, _ := v.(string); s != "" && !strings.Contains(s, "*") {
				return s, "", ""
			}
		}
		return "", "streamSettings.realitySettings.serverNames", "no entry without a wildcard"
	}
	return name, "", ""
}

func hasInlineCertificate(stream map[string]any) bool {
	certs, _ := object(stream["tlsSettings"])["certificates"].([]any)
	for _, c := range certs {
		if certificate, _ := object(c)["certificate"].([]any); len(certificate) > 0 {
			return true
		}
	}
	return false
}

func validHopPorts(s string) bool {
	m := hopPortsPattern.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	from, _ := strconv.Atoi(m[1])
	to, _ := strconv.Atoi(m[2])
	return from >= 1 && from < to && to <= 65535
}

// object returns v as a JSON object, or nil when it is not one. A read from nil is safe.
func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func lowerString(v any) string {
	s, _ := v.(string)
	return strings.ToLower(s)
}
