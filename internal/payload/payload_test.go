package payload

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSizedJSONRemainsJSONAndChangesCounter(t *testing.T) {
	g, err := New(Spec{Type: "json", Size: 512, Value: `{"deviceId":"${clientId}","sequence":"${counter}","temperature":"${random.float:60:95}","timestamp":"${timestamp}"}`}, Variables{ClientID: "device-42", Sequence: 42, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), g.Next()...)
	second := g.Next()
	if len(first) != 512 || len(second) != 512 || !json.Valid(first) || !json.Valid(second) {
		t.Fatalf("invalid sized JSON: %q", first)
	}
	if !strings.Contains(string(first), `"sequence":"1"`) || !strings.Contains(string(second), `"sequence":"2"`) {
		t.Fatalf("counter does not change: %s %s", first, second)
	}
}
func TestSeededRandomIsReproducible(t *testing.T) {
	a, _ := New(Spec{Type: "random", Size: 512}, Variables{Seed: 7, Sequence: 12})
	b, _ := New(Spec{Type: "random", Size: 512}, Variables{Seed: 7, Sequence: 12})
	if string(a.Next()) != string(b.Next()) {
		t.Fatal("random payload changed for same seed/client")
	}
}
func TestInvalidTokenOrTooSmallPayloadRejected(t *testing.T) {
	for _, spec := range []Spec{{Type: "json", Value: `{"x":"${unknown}"}`}, {Type: "static", Value: "hello", Size: 2}, {Type: "json", Value: `{"x":"${random.float:90:10}"}`}} {
		if _, err := New(spec, Variables{}); err == nil {
			t.Fatalf("accepted invalid %+v", spec)
		}
	}
}

func BenchmarkTemplate(b *testing.B) {
	g, _ := New(Spec{Type: "json", Size: 512, Value: `{"temperature":"${random.float:60:95}","sequence":"${counter}"}`}, Variables{Seed: 1})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = g.Next()
	}
}
