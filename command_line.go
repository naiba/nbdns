package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
)

// parseCommandLine reports whether the server should start and, if not, the
// exit code. Arguments used to be ignored, so `nbdns -v` started a second
// full instance on the shared data directory (stats rewrite, self-update).
func parseCommandLine(args []string, version string, out io.Writer) (run bool, exitCode int) {
	flags := flag.NewFlagSet("nbdns", flag.ContinueOnError)
	flags.SetOutput(out)
	showVersion := flags.Bool("version", false, "print the version and exit")
	flags.BoolVar(showVersion, "v", false, "print the version and exit (shorthand)")
	flags.Usage = func() {
		fmt.Fprintln(out, "Usage: nbdns [-version]")
		fmt.Fprintln(out, "Starts the DNS server using data/config.json next to the binary or in the working directory.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, 0
		}
		return false, 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(out, "unexpected argument: %s\n", flags.Arg(0))
		flags.Usage()
		return false, 2
	}
	if *showVersion {
		if version == "" {
			version = "dev"
		}
		fmt.Fprintln(out, "nbdns", version)
		return false, 0
	}
	return true, 0
}

// listenDNS binds UDP and TCP before any data, cache or self-update work, so a
// second instance fails without side effects instead of exiting 0 afterwards.
func listenDNS(addr string) (net.PacketConn, net.Listener, error) {
	packetConn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, nil, err // Already reads "listen udp <addr>: ..."
	}
	// Reuse the resolved UDP port so ":0" style addresses share one port.
	streamListener, err := net.Listen("tcp", packetConn.LocalAddr().String())
	if err != nil {
		_ = packetConn.Close()
		return nil, nil, err
	}
	return packetConn, streamListener, nil
}
