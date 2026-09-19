package desktop

import (
	"encoding/json"
	"strings"
	"testing"
)

const testSecret = "abcdefghijklmnopqrstuvwxyzABCDEFGH123456789"

func TestLaunchMaterialIsBoundedStrictAndRedacted(t *testing.T) {
	payload := `{"dataDirectory":"/private/data","logDirectory":"/private/log","loopbackPort":0,"readinessNonce":"` + testSecret + `","controlToken":"` + strings.Repeat("Z", 43) + `"}`
	material, err := ReadLaunchMaterial(strings.NewReader(payload + "\n"))
	if err != nil || material.LoopbackPort != 0 {
		t.Fatalf("ReadLaunchMaterial() = %#v, %v", material, err)
	}
	formatted, _ := json.Marshal(material)
	for _, forbidden := range []string{"/private/data", testSecret, strings.Repeat("Z", 43)} {
		if strings.Contains(string(formatted)+material.String(), forbidden) {
			t.Fatal("launch material diagnostic leaked a protected value")
		}
	}
	for _, invalid := range []string{payload + `{}` + "\n", strings.Replace(payload, `,"controlToken"`, `,"unknown":true,"controlToken"`, 1) + "\n", strings.Repeat("x", maxLaunchBytes+1)} {
		if _, err := ReadLaunchMaterial(strings.NewReader(invalid)); err == nil {
			t.Fatal("invalid launch material was accepted")
		}
	}
}

func TestNonceGateIsOneShot(t *testing.T) {
	gate, err := NewNonceGate(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if gate.Consume(strings.Repeat("x", 43)) || !gate.Consume(testSecret) || gate.Consume(testSecret) {
		t.Fatal("readiness nonce did not enforce exact one-shot consumption")
	}
}
