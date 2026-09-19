package desktop

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
)

const (
	ReadinessHeader = "X-Go-Admin-Desktop-Nonce"
	ControlHeader   = "X-Go-Admin-Desktop-Control"
	maxLaunchBytes  = 8192
)

var launchSecretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// LaunchMaterial is delivered over the sidecar's inherited stdin. Formatting deliberately
// redacts paths and secrets because process failures are commonly logged by the native host.
type LaunchMaterial struct {
	DataDirectory  string
	LogDirectory   string
	LoopbackPort   uint16
	ReadinessNonce string
	ControlToken   string
}

func (LaunchMaterial) String() string               { return "desktop.LaunchMaterial{redacted}" }
func (value LaunchMaterial) GoString() string       { return value.String() }
func (LaunchMaterial) MarshalJSON() ([]byte, error) { return []byte(`{"values":"redacted"}`), nil }

type launchWire struct {
	DataDirectory  string `json:"dataDirectory"`
	LogDirectory   string `json:"logDirectory"`
	LoopbackPort   uint16 `json:"loopbackPort"`
	ReadinessNonce string `json:"readinessNonce"`
	ControlToken   string `json:"controlToken"`
}

// ReadLaunchMaterial accepts exactly one bounded JSON value and rejects trailing input.
func ReadLaunchMaterial(reader io.Reader) (LaunchMaterial, error) {
	if reader == nil {
		return LaunchMaterial{}, errors.New("desktop launch input is required")
	}
	limited := io.LimitReader(reader, maxLaunchBytes+1)
	buffered := bufio.NewReader(limited)
	payload, err := buffered.ReadBytes('\n')
	if err != nil || len(payload) < 2 || len(payload) > maxLaunchBytes || payload[len(payload)-1] != '\n' {
		return LaunchMaterial{}, errors.New("desktop launch input is invalid")
	}
	payload = payload[:len(payload)-1]
	defer clear(payload)
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var wire launchWire
	if err := decoder.Decode(&wire); err != nil {
		return LaunchMaterial{}, errors.New("desktop launch input is invalid")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return LaunchMaterial{}, err
	}
	value := LaunchMaterial{
		DataDirectory: wire.DataDirectory, LogDirectory: wire.LogDirectory,
		LoopbackPort: wire.LoopbackPort, ReadinessNonce: wire.ReadinessNonce,
		ControlToken: wire.ControlToken,
	}
	if err := value.Validate(); err != nil {
		return LaunchMaterial{}, err
	}
	return value, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("desktop launch input is invalid")
	}
	return nil
}

func (value LaunchMaterial) Validate() error {
	if strings.TrimSpace(value.DataDirectory) == "" || strings.TrimSpace(value.LogDirectory) == "" || value.LoopbackPort != 0 ||
		!launchSecretPattern.MatchString(value.ReadinessNonce) || !launchSecretPattern.MatchString(value.ControlToken) ||
		subtle.ConstantTimeCompare([]byte(value.ReadinessNonce), []byte(value.ControlToken)) == 1 {
		return errors.New("desktop launch material is invalid")
	}
	return nil
}

func LoopbackAddress(port uint16) (string, error) {
	if port == 0 {
		return "", errors.New("desktop loopback port is required")
	}
	return net.JoinHostPort("127.0.0.1", stringPort(port)), nil
}

func stringPort(port uint16) string {
	const digits = "0123456789"
	buffer := [5]byte{}
	position := len(buffer)
	for port > 0 {
		position--
		buffer[position] = digits[port%10]
		port /= 10
	}
	return string(buffer[position:])
}

// NonceGate consumes a matching readiness nonce at most once.
type NonceGate struct {
	nonce string
	used  atomic.Bool
}

func NewNonceGate(nonce string) (*NonceGate, error) {
	if !launchSecretPattern.MatchString(nonce) {
		return nil, errors.New("desktop readiness nonce is invalid")
	}
	return &NonceGate{nonce: nonce}, nil
}

func (gate *NonceGate) Consume(candidate string) bool {
	if gate == nil || gate.used.Load() || subtle.ConstantTimeCompare([]byte(gate.nonce), []byte(candidate)) != 1 {
		return false
	}
	return gate.used.CompareAndSwap(false, true)
}

func MatchesControl(expected, candidate string) bool {
	return launchSecretPattern.MatchString(expected) && subtle.ConstantTimeCompare([]byte(expected), []byte(candidate)) == 1
}
