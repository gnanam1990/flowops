package ascpbearer

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSignerBindingVersionPreservesLegacyInputEncoding(t *testing.T) {
	legacy, err := json.Marshal(ActivationInput{SignerKeyID: "legacy-key", KeyEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacy, []byte("signerBindingVersion")) {
		t.Fatalf("legacy zero binding version changed permanent input bytes: %s", legacy)
	}
	current, err := json.Marshal(ActivationInput{SignerBindingVersion: 2, SignerKeyID: "current-key", KeyEpoch: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(current, []byte(`"signerBindingVersion":2`)) {
		t.Fatalf("current binding version is absent: %s", current)
	}
}
