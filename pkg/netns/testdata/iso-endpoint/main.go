// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"
)

const (
	exitUsage     = 2
	exitOperation = 3
	ioTimeout     = 2 * time.Second
)

type recordFlags map[string]netip.Addr

func (r *recordFlags) String() string { return "DNS name=IPv4 records" }

func (r *recordFlags) Set(raw string) error {
	name, value, ok := strings.Cut(raw, "=")
	if !ok {
		return fmt.Errorf("record must be name=IPv4")
	}
	addr, err := netip.ParseAddr(value)
	if err != nil || !addr.Is4() {
		return fmt.Errorf("record address must be IPv4")
	}
	(*r)[canonicalDNSName(name)] = addr
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fail(exitUsage, "missing subcommand")
	}
	var err error
	switch os.Args[1] {
	case "listen":
		err = runListen(os.Args[2:])
	case "connect":
		err = runConnect(os.Args[2:], false)
	case "dns":
		err = runDNS(os.Args[2:])
	case "spoof":
		err = runConnect(os.Args[2:], true)
	default:
		fail(exitUsage, "unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fail(exitOperation, "%v", err)
	}
}

func runListen(args []string) error {
	flags := flag.NewFlagSet("listen", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	address := flags.String("address", "", "listen address")
	records := recordFlags{}
	flags.Var(&records, "dns-record", "DNS name=IPv4 record (repeatable)")
	if err := flags.Parse(args); err != nil || *address == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: listen --address host:port [--dns-record name=IPv4]")
	}
	if len(records) != 0 {
		return serveDNS(*address, records)
	}
	return serveTCP(*address)
}

func serveTCP(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	writeJSON(map[string]any{"ready": true, "address": listener.Addr().String()})
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return acceptErr
		}
		go func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(ioTimeout))
			_, _ = bufio.NewReader(conn).ReadString('\n')
			remote, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
			_ = json.NewEncoder(conn).Encode(map[string]string{"remote": remote})
		}()
	}
}

func runConnect(args []string, requireSource bool) error {
	name := "connect"
	if requireSource {
		name = "spoof"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	address := flags.String("address", "", "remote address")
	source := flags.String("source", "", "source IP")
	wantRemote := flags.String("want-remote", "", "source IP the listener must observe")
	if err := flags.Parse(args); err != nil || *address == "" || flags.NArg() != 0 || requireSource && *source == "" {
		return fmt.Errorf("usage: %s --address host:port [--source IP] [--want-remote IP]", name)
	}
	dialer := net.Dialer{Timeout: ioTimeout}
	if *source != "" {
		addr, err := netip.ParseAddr(*source)
		if err != nil {
			return fmt.Errorf("parse source: %w", err)
		}
		dialer.LocalAddr = &net.TCPAddr{IP: net.IP(addr.AsSlice())}
	}
	ctx, cancel := context.WithTimeout(context.Background(), ioTimeout)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", *address)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ioTimeout))
	if _, err := io.WriteString(conn, "ping\n"); err != nil {
		return err
	}
	var reply struct {
		Remote string `json:"remote"`
	}
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return err
	}
	if *wantRemote != "" && reply.Remote != *wantRemote {
		return fmt.Errorf("listener observed source %q, want %q", reply.Remote, *wantRemote)
	}
	writeJSON(map[string]any{"connected": true, "remoteObserved": reply.Remote})
	return nil
}

func runDNS(args []string) error {
	flags := flag.NewFlagSet("dns", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	server := flags.String("server", "", "DNS server host:port")
	name := flags.String("name", "", "DNS name")
	want := flags.String("want", "", "expected IPv4 result")
	if err := flags.Parse(args); err != nil || *server == "" || *name == "" || *want == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: dns --server host:port --name name --want IPv4")
	}
	wantAddr, err := netip.ParseAddr(*want)
	if err != nil || !wantAddr.Is4() {
		return fmt.Errorf("want must be IPv4")
	}
	id := randomDNSID()
	query, err := makeDNSQuery(id, *name)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("udp", *server, ioTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ioTimeout))
	if _, err := conn.Write(query); err != nil {
		return err
	}
	response := make([]byte, 1500)
	n, err := conn.Read(response)
	if err != nil {
		return err
	}
	got, err := parseDNSA(response[:n], id)
	if err != nil {
		return err
	}
	if got != wantAddr {
		return fmt.Errorf("DNS result %s, want %s", got, wantAddr)
	}
	writeJSON(map[string]any{"resolved": got.String()})
	return nil
}

func serveDNS(address string, records map[string]netip.Addr) error {
	packet, err := net.ListenPacket("udp", address)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		packet.Close()
		return err
	}
	defer packet.Close()
	defer listener.Close()
	writeJSON(map[string]any{"ready": true, "address": address, "dns": true})
	errCh := make(chan error, 2)
	go func() { errCh <- serveDNSPackets(packet, records) }()
	go func() { errCh <- serveDNSTCP(listener, records) }()
	return <-errCh
}

func serveDNSPackets(conn net.PacketConn, records map[string]netip.Addr) error {
	buffer := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buffer)
		if err != nil {
			return err
		}
		response := makeDNSResponse(buffer[:n], records)
		if response != nil {
			_, _ = conn.WriteTo(response, peer)
		}
	}
}

func serveDNSTCP(listener net.Listener, records map[string]netip.Addr) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(ioTimeout))
			var length [2]byte
			if _, err := io.ReadFull(conn, length[:]); err != nil {
				return
			}
			query := make([]byte, binary.BigEndian.Uint16(length[:]))
			if _, err := io.ReadFull(conn, query); err != nil {
				return
			}
			response := makeDNSResponse(query, records)
			if response == nil || len(response) > 65535 {
				return
			}
			binary.BigEndian.PutUint16(length[:], uint16(len(response)))
			_, _ = conn.Write(append(length[:], response...))
		}()
	}
}

func makeDNSQuery(id uint16, name string) ([]byte, error) {
	encoded, err := encodeDNSName(name)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 12, 12+len(encoded)+4)
	binary.BigEndian.PutUint16(out[0:2], id)
	binary.BigEndian.PutUint16(out[2:4], 0x0100)
	binary.BigEndian.PutUint16(out[4:6], 1)
	out = append(out, encoded...)
	out = append(out, 0, 1, 0, 1)
	return out, nil
}

func makeDNSResponse(query []byte, records map[string]netip.Addr) []byte {
	if len(query) < 12 || binary.BigEndian.Uint16(query[4:6]) != 1 {
		return nil
	}
	name, end, err := decodeDNSName(query, 12)
	if err != nil || end+4 > len(query) {
		return nil
	}
	questionEnd := end + 4
	addr, found := records[canonicalDNSName(name)]
	out := append([]byte(nil), query[:questionEnd]...)
	flags := uint16(0x8180)
	answers := uint16(1)
	if !found {
		flags = 0x8183
		answers = 0
	}
	binary.BigEndian.PutUint16(out[2:4], flags)
	binary.BigEndian.PutUint16(out[6:8], answers)
	binary.BigEndian.PutUint16(out[8:10], 0)
	binary.BigEndian.PutUint16(out[10:12], 0)
	if !found {
		return out
	}
	out = append(out, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4)
	out = append(out, addr.AsSlice()...)
	return out
}

func parseDNSA(response []byte, id uint16) (netip.Addr, error) {
	if len(response) < 12 || binary.BigEndian.Uint16(response[:2]) != id {
		return netip.Addr{}, fmt.Errorf("invalid DNS response")
	}
	if rcode := binary.BigEndian.Uint16(response[2:4]) & 0x000f; rcode != 0 {
		return netip.Addr{}, fmt.Errorf("DNS response code %d", rcode)
	}
	questions := int(binary.BigEndian.Uint16(response[4:6]))
	answers := int(binary.BigEndian.Uint16(response[6:8]))
	offset := 12
	for range questions {
		_, end, err := decodeDNSName(response, offset)
		if err != nil || end+4 > len(response) {
			return netip.Addr{}, fmt.Errorf("invalid DNS question")
		}
		offset = end + 4
	}
	for range answers {
		_, end, err := decodeDNSName(response, offset)
		if err != nil || end+10 > len(response) {
			return netip.Addr{}, fmt.Errorf("invalid DNS answer")
		}
		typeCode := binary.BigEndian.Uint16(response[end : end+2])
		class := binary.BigEndian.Uint16(response[end+2 : end+4])
		length := int(binary.BigEndian.Uint16(response[end+8 : end+10]))
		offset = end + 10
		if offset+length > len(response) {
			return netip.Addr{}, fmt.Errorf("truncated DNS answer")
		}
		if typeCode == 1 && class == 1 && length == 4 {
			return netip.AddrFrom4([4]byte(response[offset : offset+4])), nil
		}
		offset += length
	}
	return netip.Addr{}, errors.New("DNS response has no IPv4 answer")
}

func encodeDNSName(name string) ([]byte, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" {
		return []byte{0}, nil
	}
	var out []byte
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, fmt.Errorf("invalid DNS name")
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0), nil
}

func decodeDNSName(message []byte, offset int) (string, int, error) {
	var labels []string
	next := offset
	jumped := false
	for steps := 0; steps < 128; steps++ {
		if offset >= len(message) {
			return "", 0, io.ErrUnexpectedEOF
		}
		length := int(message[offset])
		if length&0xc0 == 0xc0 {
			if offset+1 >= len(message) {
				return "", 0, io.ErrUnexpectedEOF
			}
			if !jumped {
				next = offset + 2
				jumped = true
			}
			offset = int(binary.BigEndian.Uint16(message[offset:offset+2]) & 0x3fff)
			continue
		}
		offset++
		if length == 0 {
			if !jumped {
				next = offset
			}
			return strings.Join(labels, ".") + ".", next, nil
		}
		if length > 63 || offset+length > len(message) {
			return "", 0, fmt.Errorf("invalid DNS label")
		}
		labels = append(labels, string(message[offset:offset+length]))
		offset += length
	}
	return "", 0, fmt.Errorf("DNS compression loop")
}

func canonicalDNSName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), ".")) + "."
}

func randomDNSID() uint16 {
	var raw [2]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return binary.BigEndian.Uint16(raw[:])
	}
	return uint16(time.Now().UnixNano())
}

func writeJSON(value any) {
	_ = json.NewEncoder(os.Stdout).Encode(value)
}

func fail(code int, format string, args ...any) {
	_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"error": fmt.Sprintf(format, args...)})
	os.Exit(code)
}
