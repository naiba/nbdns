package main

import (
	"bytes"
	"net"
	"strings"
	"testing"
)

func TestCommandLineVersionDoesNotStartServer(t *testing.T) {
	for _, args := range [][]string{{"-v"}, {"-version"}, {"--version"}} {
		var out bytes.Buffer
		run, code := parseCommandLine(args, "2.1.1", &out)
		if run || code != 0 {
			t.Errorf("%v: run=%v code=%d, want version only", args, run, code)
		}
		if got := strings.TrimSpace(out.String()); got != "nbdns 2.1.1" {
			t.Errorf("%v: output %q", args, got)
		}
	}
	var out bytes.Buffer
	if run, code := parseCommandLine([]string{"-v"}, "", &out); run || code != 0 || strings.TrimSpace(out.String()) != "nbdns dev" {
		t.Errorf("unversioned build: run=%v code=%d output=%q", run, code, out.String())
	}
}

func TestCommandLineRejectsUnknownArguments(t *testing.T) {
	for _, args := range [][]string{{"--bogus"}, {"serve"}} {
		var out bytes.Buffer
		run, code := parseCommandLine(args, "2.1.1", &out)
		if run || code != 2 {
			t.Errorf("%v: run=%v code=%d, want usage error", args, run, code)
		}
		if !strings.Contains(out.String(), "-version") {
			t.Errorf("%v: usage missing from %q", args, out.String())
		}
	}
	var out bytes.Buffer
	if run, code := parseCommandLine([]string{"-h"}, "2.1.1", &out); run || code != 0 || !strings.Contains(out.String(), "-version") {
		t.Errorf("-h: run=%v code=%d output=%q", run, code, out.String())
	}
	if run, _ := parseCommandLine(nil, "2.1.1", &out); !run {
		t.Error("no arguments must start the server")
	}
}

// A second instance (e.g. started by mistake) must fail before touching the
// shared data directory: binding happens first and reports the busy port.
func TestListenDNSFailsFastWhenPortIsBusy(t *testing.T) {
	busy, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if _, _, err := listenDNS(busy.LocalAddr().String()); err == nil || !strings.Contains(err.Error(), "udp") {
		t.Fatalf("busy UDP port: err = %v", err)
	}

	packet, stream, err := listenDNS("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if packet.LocalAddr().String() != stream.Addr().String() {
		t.Errorf("UDP %s and TCP %s must share one port", packet.LocalAddr(), stream.Addr())
	}
	_ = packet.Close()
	_ = stream.Close()
}

func TestListenDNSReleasesUDPWhenTCPIsBusy(t *testing.T) {
	busyTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busyTCP.Close()
	addr := busyTCP.Addr().String()
	if _, _, err := listenDNS(addr); err == nil || !strings.Contains(err.Error(), "tcp") {
		t.Fatalf("busy TCP port: err = %v", err)
	}
	// The UDP socket opened before the TCP failure must have been closed.
	released, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("UDP socket leaked after TCP bind failure: %v", err)
	}
	_ = released.Close()
}
