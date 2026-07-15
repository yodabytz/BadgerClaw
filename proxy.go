package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// resolveProxy picks the proxy URL to use. Precedence: BADGERCLAW_PROXY, then
// the saved config, then the standard HTTP(S)_PROXY / ALL_PROXY environment.
// An empty result means "no explicit proxy" and Go's default env handling
// (HTTP_PROXY / HTTPS_PROXY) still applies.
func resolveProxy(cfg Config) string {
	for _, v := range []string{
		os.Getenv("BADGERCLAW_PROXY"),
		cfg.Proxy,
		os.Getenv("ALL_PROXY"),
		os.Getenv("all_proxy"),
	} {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// buildProxyTransport returns an *http.Transport routed through the given proxy
// URL. Empty means no explicit proxy, in which case the transport still honors
// HTTP_PROXY / HTTPS_PROXY from the environment (Go's default). Supported
// schemes: http, https, socks5, socks5h (all native to Go), and socks4, socks4a
// (implemented here).
func buildProxyTransport(proxyURL string) (*http.Transport, error) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	if proxyURL == "" {
		return base, nil
	}

	// Allow "host:port" shorthand -> assume socks5.
	if !strings.Contains(proxyURL, "://") {
		proxyURL = "socks5://" + proxyURL
	}

	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy %q: %w", proxyURL, err)
	}

	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		// Go's transport dials socks5 natively and http/https as a CONNECT proxy.
		base.Proxy = http.ProxyURL(u)
	case "socks4", "socks4a":
		d := &socks4Dialer{
			proxyAddr: u.Host,
			remoteDNS: strings.EqualFold(u.Scheme, "socks4a"),
			userID:    u.User.Username(),
		}
		base.Proxy = nil
		base.DialContext = d.DialContext
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use http, https, socks5, socks5h, socks4, socks4a)", u.Scheme)
	}
	return base, nil
}

// socks4Dialer implements SOCKS4 and SOCKS4a CONNECT, which Go's standard
// library does not provide. SOCKS4a sends the hostname to the proxy; plain
// SOCKS4 resolves it locally to an IPv4 address first.
type socks4Dialer struct {
	proxyAddr string
	remoteDNS bool
	userID    string
}

func (d *socks4Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("socks4: unsupported network %q", network)
	}

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("socks4: bad port %q", portStr)
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", d.proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("socks4: cannot reach proxy: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}

	var ip4 net.IP
	sendHostname := false
	if parsed := net.ParseIP(host); parsed != nil && parsed.To4() != nil {
		ip4 = parsed.To4()
	} else if d.remoteDNS {
		// SOCKS4a: 0.0.0.x (x != 0) tells the proxy to resolve the hostname.
		ip4 = net.IPv4(0, 0, 0, 1).To4()
		sendHostname = true
	} else {
		addrs, rerr := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if rerr != nil || len(addrs) == 0 {
			conn.Close()
			return nil, fmt.Errorf("socks4: cannot resolve %q (use socks4a for remote DNS): %w", host, rerr)
		}
		ip4 = addrs[0].To4()
	}

	req := []byte{0x04, 0x01}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	req = append(req, ip4...)
	req = append(req, []byte(d.userID)...)
	req = append(req, 0x00)
	if sendHostname {
		req = append(req, []byte(host)...)
		req = append(req, 0x00)
	}
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks4: write failed: %w", err)
	}

	resp := make([]byte, 8)
	if _, err := bufio.NewReader(conn).Read(resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks4: no reply from proxy: %w", err)
	}
	if resp[0] != 0x00 {
		conn.Close()
		return nil, errors.New("socks4: malformed proxy reply")
	}
	switch resp[1] {
	case 0x5a: // granted
		_ = conn.SetDeadline(time.Time{})
		return conn, nil
	case 0x5b:
		conn.Close()
		return nil, errors.New("socks4: request rejected or failed")
	case 0x5c, 0x5d:
		conn.Close()
		return nil, errors.New("socks4: proxy could not reach identd/userid")
	default:
		conn.Close()
		return nil, fmt.Errorf("socks4: proxy returned status 0x%02x", resp[1])
	}
}

// cmdProxy views, sets, or clears the saved proxy.
func cmdProxy(args []string) error {
	cfg, _ := loadConfig()
	if len(args) == 0 {
		active := resolveProxy(cfg)
		if active == "" {
			fmt.Println("No proxy set. Traffic goes direct (HTTP_PROXY/HTTPS_PROXY still apply).")
		} else {
			fmt.Printf("Proxy: %s\n", active)
			if os.Getenv("BADGERCLAW_PROXY") != "" {
				fmt.Println("(from BADGERCLAW_PROXY; overrides the saved value)")
			}
		}
		fmt.Println("\nSet:   badgerclaw proxy socks5://127.0.0.1:9050")
		fmt.Println("Clear: badgerclaw proxy off")
		fmt.Println("Schemes: http, https, socks5, socks5h, socks4, socks4a")
		return nil
	}

	value := strings.TrimSpace(args[0])
	if value == "off" || value == "none" || value == "clear" {
		cfg.Proxy = ""
		if err := saveConfig(cfg); err != nil {
			return err
		}
		fmt.Println("Proxy cleared.")
		return nil
	}

	if _, err := buildProxyTransport(value); err != nil {
		return err
	}
	cfg.Proxy = value
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("Proxy set to %s.\n", value)
	return nil
}
