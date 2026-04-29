package main

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/textproto"
	"os"
	"strings"
	"time"
)

const version = "0.4.0"

type Config struct {
	Server        string
	Port          int
	From          string
	To            []string
	Cc            []string
	Bcc           []string
	Subject       string
	Body          string
	User          string
	Pass          string
	TLS           string // auto | yes | no
	SSL           bool   // direct TLS on connect (port 465)
	AddHeader     []string
	ReplaceHeader []string
	Verbose       bool
}

// msgHeader is an ordered name/value pair for message headers.
type msgHeader struct{ name, value string }

// msgHeaders is an ordered list of headers with set/add helpers.
type msgHeaders []msgHeader

// set replaces the first header matching name (canonical), or appends it.
func (h *msgHeaders) set(name, value string) {
	canon := textproto.CanonicalMIMEHeaderKey(name)
	for i, hdr := range *h {
		if textproto.CanonicalMIMEHeaderKey(hdr.name) == canon {
			(*h)[i].value = value
			return
		}
	}
	*h = append(*h, msgHeader{name, value})
}

// add always appends a header regardless of duplicates.
func (h *msgHeaders) add(name, value string) {
	*h = append(*h, msgHeader{name, value})
}

// writeTo writes all headers followed by the blank separator line.
func (h msgHeaders) writeTo(w io.Writer) {
	for _, hdr := range h {
		fmt.Fprintf(w, "%s: %s\r\n", hdr.name, hdr.value)
	}
	fmt.Fprintf(w, "\r\n")
}

func main() {
	cfg := parseFlags()

	if cfg.Server == "" {
		fmt.Fprintln(os.Stderr, "error: -s SERVER is required")
		flag.Usage()
		os.Exit(1)
	}
	if cfg.From == "" {
		fmt.Fprintln(os.Stderr, "error: -f FROM is required")
		os.Exit(1)
	}
	if len(cfg.To) == 0 && len(cfg.Cc) == 0 && len(cfg.Bcc) == 0 {
		fmt.Fprintln(os.Stderr, "error: at least one recipient (-t, -cc, or -bcc) is required")
		os.Exit(1)
	}

	if err := sendMail(cfg); err != nil {
		log.Fatalf("send failed: %v", err)
	}
	fmt.Println("Email sent successfully.")
}

func parseFlags() *Config {
	cfg := &Config{}

	flag.StringVar(&cfg.Server, "s", "", "SMTP server (host or host:port)")
	flag.IntVar(&cfg.Port, "port", 25, "SMTP port (overridden if host:port given with -s)")
	flag.StringVar(&cfg.From, "f", "", "Sender address")
	flag.StringVar(&cfg.Subject, "u", "", "Subject line")
	flag.StringVar(&cfg.Body, "m", "", "Message body (plain text)")
	flag.StringVar(&cfg.User, "xu", "", "SMTP auth username")
	flag.StringVar(&cfg.Pass, "xp", "", "SMTP auth password")
	flag.StringVar(&cfg.TLS, "tls", "auto", "STARTTLS mode: auto, yes, no")
	flag.BoolVar(&cfg.SSL, "ssl", false, "Use direct TLS on connect (SMTPS, port 465)")
	flag.BoolVar(&cfg.Verbose, "v", false, "Verbose SMTP session output")
	flag.Func("add-header", `Add a header, e.g. --add-header "X-Mailer: gosmtp-cli"`, func(s string) error {
		cfg.AddHeader = append(cfg.AddHeader, s)
		return nil
	})
	flag.Func("replace-header", `Replace a built-in header, e.g. --replace-header "From: Alice <a@b.com>"`, func(s string) error {
		cfg.ReplaceHeader = append(cfg.ReplaceHeader, s)
		return nil
	})

	// Repeatable / comma-separated recipient flags
	flag.Func("t", "Recipient (repeatable, comma-separated)", func(s string) error {
		cfg.To = appendAddrs(cfg.To, s)
		return nil
	})
	flag.Func("cc", "CC recipient (repeatable, comma-separated)", func(s string) error {
		cfg.Cc = appendAddrs(cfg.Cc, s)
		return nil
	})
	flag.Func("bcc", "BCC recipient (repeatable, comma-separated)", func(s string) error {
		cfg.Bcc = appendAddrs(cfg.Bcc, s)
		return nil
	})

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "gosmtp-cli v%s — send email from the command line\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage: gosmtp-cli -s SERVER -f FROM -t TO [options]\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	// Support host:port in -s
	if strings.Contains(cfg.Server, ":") {
		host, portStr, err := net.SplitHostPort(cfg.Server)
		if err == nil {
			cfg.Server = host
			fmt.Sscanf(portStr, "%d", &cfg.Port)
		}
	}

	// Port 465 implies direct TLS
	if cfg.Port == 465 {
		cfg.SSL = true
	}

	return cfg
}

// appendAddrs splits comma-separated addresses and appends them to dst.
func appendAddrs(dst []string, s string) []string {
	for _, addr := range strings.Split(s, ",") {
		if a := strings.TrimSpace(addr); a != "" {
			dst = append(dst, a)
		}
	}
	return dst
}

func sendMail(cfg *Config) error {
	addr := fmt.Sprintf("%s:%d", cfg.Server, cfg.Port)

	rawConn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}

	// Upgrade to TLS immediately for SMTPS (port 465)
	var conn net.Conn = rawConn
	if cfg.SSL {
		conn = tls.Client(rawConn, &tls.Config{ServerName: cfg.Server})
		if cfg.Verbose {
			fmt.Println("[TLS handshake complete — direct SSL]")
		}
	}

	c := textproto.NewConn(conn)
	defer func() { c.Close() }()

	if _, _, err := c.ReadResponse(220); err != nil {
		return fmt.Errorf("greeting: %w", err)
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}

	authMethods, starttlsOK, ehloOK := ehlo(c, hostname, cfg.Verbose)

	if !ehloOK {
		if err := smtpCmd(c, cfg.Verbose, fmt.Sprintf("HELO %s", hostname), 250); err != nil {
			return fmt.Errorf("HELO: %w", err)
		}
	}

	// STARTTLS upgrade (skip if already on direct TLS)
	if !cfg.SSL && ehloOK {
		switch cfg.TLS {
		case "yes":
			if !starttlsOK {
				return fmt.Errorf("--tls=yes but server did not advertise STARTTLS")
			}
			if err := doStartTLS(c, conn, rawConn, cfg, hostname, &authMethods); err != nil {
				return err
			}
		case "auto":
			if starttlsOK {
				if err := doStartTLS(c, conn, rawConn, cfg, hostname, &authMethods); err != nil {
					return err
				}
			}
		}
	}

	// Authenticate if credentials provided
	if cfg.User != "" {
		if !ehloOK {
			return fmt.Errorf("server did not accept EHLO; cannot authenticate")
		}
		if len(authMethods) == 0 {
			return fmt.Errorf("server advertised no AUTH methods")
		}
		if err := authenticate(c, cfg, authMethods); err != nil {
			return err
		}
	}

	if err := smtpCmd(c, cfg.Verbose, fmt.Sprintf("MAIL FROM:<%s>", cfg.From), 250); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}

	// RCPT TO for all recipients (To + Cc + Bcc all go to the envelope)
	for _, rcpt := range allRecipients(cfg) {
		if err := smtpCmd(c, cfg.Verbose, fmt.Sprintf("RCPT TO:<%s>", rcpt), 250); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", rcpt, err)
		}
	}

	if err := smtpCmd(c, cfg.Verbose, "DATA", 354); err != nil {
		return fmt.Errorf("DATA: %w", err)
	}

	// Build default headers in send order.
	hdrs := msgHeaders{
		{"From", cfg.From},
		{"Date", time.Now().Format(time.RFC1123Z)},
		{"Message-ID", fmt.Sprintf("<%d.%d@%s>", time.Now().UnixNano(), os.Getpid(), hostname)},
		{"Subject", cfg.Subject},
		{"Content-Type", "text/plain; charset=UTF-8"},
	}
	if len(cfg.To) > 0 {
		hdrs.add("To", strings.Join(cfg.To, ", "))
	}
	if len(cfg.Cc) > 0 {
		hdrs.add("Cc", strings.Join(cfg.Cc, ", "))
	}
	// Bcc intentionally omitted from headers

	// Apply --replace-header (replaces existing by name) then --add-header (always appends).
	for _, h := range cfg.ReplaceHeader {
		name, value, ok := parseHeaderArg(h)
		if !ok {
			return fmt.Errorf("invalid --replace-header %q: expected \"Name: value\"", h)
		}
		hdrs.set(name, value)
	}
	for _, h := range cfg.AddHeader {
		name, value, ok := parseHeaderArg(h)
		if !ok {
			return fmt.Errorf("invalid --add-header %q: expected \"Name: value\"", h)
		}
		hdrs.add(name, value)
	}

	w := c.DotWriter()
	hdrs.writeTo(w)
	fmt.Fprintf(w, "%s\r\n", cfg.Body)
	if err := w.Close(); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}

	if _, _, err := c.ReadResponse(250); err != nil {
		return fmt.Errorf("message accepted: %w", err)
	}

	_ = c.PrintfLine("QUIT")
	return nil
}

// doStartTLS sends STARTTLS, upgrades the connection, and re-EHLOs.
// It updates *c and *authMethods in place via the pointer to the textproto.Conn.
func doStartTLS(c *textproto.Conn, conn, rawConn net.Conn, cfg *Config, hostname string, authMethods *[]string) error {
	if err := smtpCmd(c, cfg.Verbose, "STARTTLS", 220); err != nil {
		return fmt.Errorf("STARTTLS: %w", err)
	}

	tlsConn := tls.Client(rawConn, &tls.Config{ServerName: cfg.Server})
	if cfg.Verbose {
		fmt.Println("[TLS handshake complete — STARTTLS]")
	}

	// Rebuild textproto.Conn on the upgraded connection.
	// The caller's defer uses a closure so it will pick up the new value.
	*c = *textproto.NewConn(tlsConn)

	newMethods, _, _ := ehlo(c, hostname, cfg.Verbose)
	*authMethods = newMethods
	return nil
}

// allRecipients returns the combined envelope recipient list (To + Cc + Bcc).
func allRecipients(cfg *Config) []string {
	return append(append(append([]string{}, cfg.To...), cfg.Cc...), cfg.Bcc...)
}

// ehlo sends EHLO and returns advertised AUTH methods, STARTTLS support, and success.
func ehlo(c *textproto.Conn, hostname string, verbose bool) (authMethods []string, starttls bool, ok bool) {
	if verbose {
		fmt.Printf("C: EHLO %s\n", hostname)
	}
	if err := c.PrintfLine("EHLO %s", hostname); err != nil {
		return nil, false, false
	}
	code, msg, err := c.ReadResponse(250)
	if verbose {
		fmt.Printf("S: %d %s\n", code, strings.ReplaceAll(msg, "\n", "\nS:  "))
	}
	if err != nil {
		return nil, false, false
	}

	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		if upper == "STARTTLS" {
			starttls = true
		}
		if strings.HasPrefix(upper, "AUTH ") {
			authMethods = strings.Fields(line[5:])
		}
	}
	return authMethods, starttls, true
}

// authenticate picks the best available method.
func authenticate(c *textproto.Conn, cfg *Config, advertised []string) error {
	supported := map[string]bool{}
	for _, m := range advertised {
		supported[strings.ToUpper(m)] = true
	}
	switch {
	case supported["CRAM-MD5"]:
		return authCramMD5(c, cfg)
	case supported["PLAIN"]:
		return authPlain(c, cfg)
	case supported["LOGIN"]:
		return authLogin(c, cfg)
	}
	return fmt.Errorf("no supported AUTH method in server's list: %v", advertised)
}

func authPlain(c *textproto.Conn, cfg *Config) error {
	creds := base64.StdEncoding.EncodeToString([]byte("\x00" + cfg.User + "\x00" + cfg.Pass))
	if cfg.Verbose {
		fmt.Println("C: AUTH PLAIN [credentials hidden]")
	}
	if err := c.PrintfLine("AUTH PLAIN %s", creds); err != nil {
		return err
	}
	code, msg, err := c.ReadResponse(235)
	if cfg.Verbose {
		fmt.Printf("S: %d %s\n", code, msg)
	}
	if err != nil {
		return fmt.Errorf("AUTH PLAIN: %w", err)
	}
	return nil
}

func authLogin(c *textproto.Conn, cfg *Config) error {
	if cfg.Verbose {
		fmt.Println("C: AUTH LOGIN")
	}
	if err := c.PrintfLine("AUTH LOGIN"); err != nil {
		return err
	}
	if code, msg, err := c.ReadResponse(334); err != nil {
		if cfg.Verbose {
			fmt.Printf("S: %d %s\n", code, msg)
		}
		return fmt.Errorf("AUTH LOGIN: %w", err)
	}
	if cfg.Verbose {
		fmt.Println("C: [username]")
	}
	if err := c.PrintfLine("%s", base64.StdEncoding.EncodeToString([]byte(cfg.User))); err != nil {
		return err
	}
	if code, msg, err := c.ReadResponse(334); err != nil {
		if cfg.Verbose {
			fmt.Printf("S: %d %s\n", code, msg)
		}
		return fmt.Errorf("AUTH LOGIN username: %w", err)
	}
	if cfg.Verbose {
		fmt.Println("C: [password]")
	}
	if err := c.PrintfLine("%s", base64.StdEncoding.EncodeToString([]byte(cfg.Pass))); err != nil {
		return err
	}
	code, msg, err := c.ReadResponse(235)
	if cfg.Verbose {
		fmt.Printf("S: %d %s\n", code, msg)
	}
	if err != nil {
		return fmt.Errorf("AUTH LOGIN password: %w", err)
	}
	return nil
}

func authCramMD5(c *textproto.Conn, cfg *Config) error {
	if cfg.Verbose {
		fmt.Println("C: AUTH CRAM-MD5")
	}
	if err := c.PrintfLine("AUTH CRAM-MD5"); err != nil {
		return err
	}
	code, challenge, err := c.ReadResponse(334)
	if cfg.Verbose {
		fmt.Printf("S: %d %s\n", code, challenge)
	}
	if err != nil {
		return fmt.Errorf("AUTH CRAM-MD5: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(challenge)
	if err != nil {
		return fmt.Errorf("decode CRAM-MD5 challenge: %w", err)
	}
	h := hmac.New(md5.New, []byte(cfg.Pass))
	h.Write(decoded)
	response := fmt.Sprintf("%s %x", cfg.User, h.Sum(nil))
	if cfg.Verbose {
		fmt.Println("C: [cram-md5 response]")
	}
	if err := c.PrintfLine("%s", base64.StdEncoding.EncodeToString([]byte(response))); err != nil {
		return err
	}
	code, msg, err := c.ReadResponse(235)
	if cfg.Verbose {
		fmt.Printf("S: %d %s\n", code, msg)
	}
	if err != nil {
		return fmt.Errorf("AUTH CRAM-MD5 response: %w", err)
	}
	return nil
}

// parseHeaderArg splits "Name: value" into its parts.
func parseHeaderArg(s string) (name, value string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i < 1 {
		return "", "", false
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), true
}

// smtpCmd sends a command and expects a specific response code.
func smtpCmd(c *textproto.Conn, verbose bool, cmd string, expectCode int) error {
	if verbose {
		fmt.Printf("C: %s\n", cmd)
	}
	if err := c.PrintfLine("%s", cmd); err != nil {
		return err
	}
	code, msg, err := c.ReadResponse(expectCode)
	if verbose {
		fmt.Printf("S: %d %s\n", code, msg)
	}
	return err
}
