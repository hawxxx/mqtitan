package main

import (
	"testing"
)

func TestQuickRejectsWrappedQoS(t *testing.T) {
	if err := quick([]string{"--qos", "256", "--duration", "1ms"}); err == nil {
		t.Fatal("qos 256 wrapped into valid qos")
	}
}
func TestQuickRejectsUnknownArguments(t *testing.T) {
	if err := quick([]string{"--duration", "1ms", "unexpected"}); err == nil {
		t.Fatal("silently ignored positional arguments")
	}
}
func TestRemoteListenRequiresAuthentication(t *testing.T) {
	if err := checkListen("0.0.0.0:8080", "", false); err == nil {
		t.Fatal("remote control exposed without authentication")
	}
	if err := checkListen("127.0.0.1:8080", "", false); err != nil {
		t.Fatal(err)
	}
}
