package main

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var version = "dev"

type Config struct {
	// Connection
	Server       string
	Port         int
	TLS          string // auto | yes | no
	SSL          bool   // direct TLS on connect (port 465)
	Timeout      int    // seconds
	NoVerifyCert bool

	// Sender & Recipients
	From string
	To   []string
	Cc   []string
	Bcc  []string

	// Message
	Subject       string
	Body          string   // plain text (-m), may be "-" for stdin or a file path
	BodyHTML      string   // HTML body (--body-html), may be "-" or a file path
	Attachments   []string // file paths (-a, repeatable)
	AddHeader     []string
	ReplaceHeader []string

	// Auth
	User string
	Pass string

	// Output
	Verbose bool
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

func printUsage() {
	fmt.Fprintf(os.Stderr, "gosmtp-cli v%s — send email from the command line\n\n", version)
	fmt.Fprintf(os.Stderr, "Usage:\n  gosmtp-cli -s SERVER -f FROM -t TO [options]\n\n")
	printFlagGroup("Connection", []string{"s", "port", "tls", "ssl", "timeout", "no-verify-cert"})
	printFlagGroup("Sender & Recipients", []string{"f", "t", "cc", "bcc"})
	printFlagGroup("Message", []string{"u", "m", "body-html", "a", "add-header", "replace-header"})
	printFlagGroup("Authentication", []string{"xu", "xp"})
	printFlagGroup("Output", []string{"v"})
}

func printFlagGroup(title string, names []string) {
	fmt.Fprintf(os.Stderr, "%s:\n", title)
	for _, name := range names {
		f := flag.Lookup(name)
		if f == nil {
			continue
		}
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
			fmt.Fprintf(os.Stderr, "  -%s\t%s (default: %s)\n", f.Name, f.Usage, f.DefValue)
		} else {
			fmt.Fprintf(os.Stderr, "  -%s\t%s\n", f.Name, f.Usage)
		}
	}
	fmt.Fprintln(os.Stderr)
}

func parseFlags() *Config {
	cfg := &Config{}

	// Connection
	flag.StringVar(&cfg.Server, "s", "", "SMTP server (host or host:port); omit to resolve via MX lookup")
	flag.IntVar(&cfg.Port, "port", 25, "SMTP port (overridden if host:port given with -s)")
	flag.StringVar(&cfg.TLS, "tls", "auto", "STARTTLS mode: auto, yes, no")
	flag.BoolVar(&cfg.SSL, "ssl", false, "Use direct TLS on connect (SMTPS, port 465)")
	flag.IntVar(&cfg.Timeout, "timeout", 30, "Connection timeout in seconds")
	flag.BoolVar(&cfg.NoVerifyCert, "no-verify-cert", false, "Skip TLS certificate verification")

	// Sender & Recipients
	flag.StringVar(&cfg.From, "f", "", "Sender address")
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

	// Message
	flag.StringVar(&cfg.Subject, "u", "", "Subject line")
	flag.StringVar(&cfg.Body, "m", "", "Plain text body (literal, file path, or - for stdin)")
	flag.StringVar(&cfg.BodyHTML, "body-html", "", "HTML body (literal, file path, or - for stdin)")
	flag.Func("a", "Attach a file (repeatable)", func(s string) error {
		cfg.Attachments = append(cfg.Attachments, s)
		return nil
	})
	flag.Func("add-header", `Add a header, e.g. -add-header "X-Mailer: gosmtp-cli"`, func(s string) error {
		cfg.AddHeader = append(cfg.AddHeader, s)
		return nil
	})
	flag.Func("replace-header", `Replace a built-in header, e.g. -replace-header "From: Alice <a@b.com>"`, func(s string) error {
		cfg.ReplaceHeader = append(cfg.ReplaceHeader, s)
		return nil
	})

	// Auth
	flag.StringVar(&cfg.User, "xu", "", "SMTP auth username")
	flag.StringVar(&cfg.Pass, "xp", "", "SMTP auth password")

	// Output
	flag.BoolVar(&cfg.Verbose, "v", false, "Verbose SMTP session output")

	flag.Usage = printUsage
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
	// MX lookup if no server specified
	if cfg.Server == "" {
		if err := resolveMX(cfg); err != nil {
			return err
		}
	}

	timeout := time.Duration(cfg.Timeout) * time.Second

	rawConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", cfg.Server, cfg.Port), timeout)
	if err != nil {
		return fmt.Errorf("connect %s:%d: %w", cfg.Server, cfg.Port, err)
	}

	tlsCfg := &tls.Config{
		ServerName:         cfg.Server,
		InsecureSkipVerify: cfg.NoVerifyCert,
	}

	var conn net.Conn = rawConn
	if cfg.SSL {
		conn = tls.Client(rawConn, tlsCfg)
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
			if err := doStartTLS(c, rawConn, tlsCfg, hostname, cfg.Verbose, &authMethods); err != nil {
				return err
			}
		case "auto":
			if starttlsOK {
				if err := doStartTLS(c, rawConn, tlsCfg, hostname, cfg.Verbose, &authMethods); err != nil {
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

	for _, rcpt := range allRecipients(cfg) {
		if err := smtpCmd(c, cfg.Verbose, fmt.Sprintf("RCPT TO:<%s>", rcpt), 250); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", rcpt, err)
		}
	}

	if err := smtpCmd(c, cfg.Verbose, "DATA", 354); err != nil {
		return fmt.Errorf("DATA: %w", err)
	}

	msg, err := composeMessage(cfg, hostname)
	if err != nil {
		return fmt.Errorf("composing message: %w", err)
	}

	w := c.DotWriter()
	if _, err := io.WriteString(w, msg); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing message: %w", err)
	}

	if _, _, err := c.ReadResponse(250); err != nil {
		return fmt.Errorf("message accepted: %w", err)
	}

	_ = c.PrintfLine("QUIT")
	return nil
}

// composeMessage builds the full RFC 2822 message string.
func composeMessage(cfg *Config, hostname string) (string, error) {
	plainBody, err := readBody(cfg.Body)
	if err != nil {
		return "", fmt.Errorf("plain body: %w", err)
	}
	htmlBody, err := readBody(cfg.BodyHTML)
	if err != nil {
		return "", fmt.Errorf("HTML body: %w", err)
	}

	var buf strings.Builder

	hdrs := msgHeaders{
		{"From", cfg.From},
		{"Date", time.Now().Format(time.RFC1123Z)},
		{"Message-ID", fmt.Sprintf("<%d.%d@%s>", time.Now().UnixNano(), os.Getpid(), hostname)},
		{"Subject", cfg.Subject},
		{"MIME-Version", "1.0"},
	}
	if len(cfg.To) > 0 {
		hdrs.add("To", strings.Join(cfg.To, ", "))
	}
	if len(cfg.Cc) > 0 {
		hdrs.add("Cc", strings.Join(cfg.Cc, ", "))
	}

	for _, h := range cfg.ReplaceHeader {
		name, value, ok := parseHeaderArg(h)
		if !ok {
			return "", fmt.Errorf("invalid -replace-header %q: expected \"Name: value\"", h)
		}
		hdrs.set(name, value)
	}
	for _, h := range cfg.AddHeader {
		name, value, ok := parseHeaderArg(h)
		if !ok {
			return "", fmt.Errorf("invalid -add-header %q: expected \"Name: value\"", h)
		}
		hdrs.add(name, value)
	}

	hasAttachments := len(cfg.Attachments) > 0
	hasBoth := plainBody != "" && htmlBody != ""

	switch {
	case hasAttachments:
		// multipart/mixed wrapping body + attachments
		boundary := newBoundary()
		hdrs.set("Content-Type", fmt.Sprintf("multipart/mixed; boundary=%q", boundary))
		hdrs.writeTo(&buf)
		if hasBoth {
			// nested multipart/alternative inside mixed
			altBoundary := newBoundary()
			fmt.Fprintf(&buf, "--%s\r\n", boundary)
			fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", altBoundary)
			writeTextPart(&buf, plainBody, "text/plain", altBoundary)
			writeTextPart(&buf, htmlBody, "text/html", altBoundary)
			fmt.Fprintf(&buf, "--%s--\r\n\r\n", altBoundary)
		} else if htmlBody != "" {
			writeTextPart(&buf, htmlBody, "text/html", boundary)
		} else {
			writeTextPart(&buf, plainBody, "text/plain", boundary)
		}
		for _, path := range cfg.Attachments {
			if err := writeAttachment(&buf, path, boundary); err != nil {
				return "", fmt.Errorf("attachment %s: %w", path, err)
			}
		}
		fmt.Fprintf(&buf, "--%s--\r\n", boundary)

	case hasBoth:
		// multipart/alternative — plain + HTML, no attachments
		boundary := newBoundary()
		hdrs.set("Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", boundary))
		hdrs.writeTo(&buf)
		writeTextPart(&buf, plainBody, "text/plain", boundary)
		writeTextPart(&buf, htmlBody, "text/html", boundary)
		fmt.Fprintf(&buf, "--%s--\r\n", boundary)

	case htmlBody != "":
		hdrs.set("Content-Type", "text/html; charset=UTF-8")
		hdrs.set("Content-Transfer-Encoding", "base64")
		hdrs.writeTo(&buf)
		writeBase64Body(&buf, htmlBody)

	default:
		hdrs.set("Content-Type", "text/plain; charset=UTF-8")
		hdrs.set("Content-Transfer-Encoding", "base64")
		hdrs.writeTo(&buf)
		writeBase64Body(&buf, plainBody)
	}

	return buf.String(), nil
}

// readBody reads the body content: "-" = stdin, existing file = file contents, else literal.
func readBody(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if s == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	if info, err := os.Stat(s); err == nil && !info.IsDir() {
		data, err := os.ReadFile(s)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return s, nil
}

// writeTextPart writes a single text/* MIME part within a multipart boundary.
func writeTextPart(buf *strings.Builder, body, contentType, boundary string) {
	fmt.Fprintf(buf, "--%s\r\n", boundary)
	fmt.Fprintf(buf, "Content-Type: %s; charset=UTF-8\r\n", contentType)
	fmt.Fprintf(buf, "Content-Transfer-Encoding: base64\r\n\r\n")
	writeBase64Body(buf, body)
}

// writeBase64Body base64-encodes body and writes it with 76-char line wrapping.
func writeBase64Body(buf *strings.Builder, body string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	for len(encoded) > 76 {
		buf.WriteString(encoded[:76])
		buf.WriteString("\r\n")
		encoded = encoded[76:]
	}
	buf.WriteString(encoded)
	buf.WriteString("\r\n")
}

// writeAttachment reads a file and writes it as a base64-encoded MIME attachment part.
func writeAttachment(buf *strings.Builder, path, boundary string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	name := filepath.Base(path)
	mimeType := detectMIME(path)

	fmt.Fprintf(buf, "--%s\r\n", boundary)
	fmt.Fprintf(buf, "Content-Type: %s; name=%q\r\n", mimeType, name)
	fmt.Fprintf(buf, "Content-Transfer-Encoding: base64\r\n")
	fmt.Fprintf(buf, "Content-Disposition: attachment; filename=%q\r\n\r\n", name)

	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 76 {
		buf.WriteString(encoded[:76])
		buf.WriteString("\r\n")
		encoded = encoded[76:]
	}
	buf.WriteString(encoded)
	buf.WriteString("\r\n")
	return nil
}

// detectMIME returns the MIME type for a file based on its extension.
func detectMIME(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	// Fallback for common types not always in the OS mime database
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	case ".gz":
		return "application/gzip"
	case ".tar":
		return "application/x-tar"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".xml":
		return "application/xml"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	}
	return "application/octet-stream"
}

// newBoundary generates a random MIME boundary string.
func newBoundary() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "----=_Part_" + hex.EncodeToString(b)
}

// resolveMX looks up the MX record for the first recipient's domain and sets cfg.Server.
func resolveMX(cfg *Config) error {
	recipient := firstRecipient(cfg)
	if recipient == "" {
		return fmt.Errorf("no -s server specified and no recipients to resolve MX from")
	}
	parts := strings.SplitN(recipient, "@", 2)
	if len(parts) != 2 {
		return fmt.Errorf("cannot resolve MX: invalid address %q", recipient)
	}
	domain := parts[1]
	records, err := net.LookupMX(domain)
	if err != nil {
		return fmt.Errorf("MX lookup for %s: %w", domain, err)
	}
	if len(records) == 0 {
		return fmt.Errorf("no MX records found for %s", domain)
	}
	cfg.Server = strings.TrimSuffix(records[0].Host, ".")
	if cfg.Verbose {
		fmt.Printf("[MX resolved: %s → %s]\n", domain, cfg.Server)
	}
	return nil
}

func firstRecipient(cfg *Config) string {
	if len(cfg.To) > 0 {
		return cfg.To[0]
	}
	if len(cfg.Cc) > 0 {
		return cfg.Cc[0]
	}
	if len(cfg.Bcc) > 0 {
		return cfg.Bcc[0]
	}
	return ""
}

// doStartTLS sends STARTTLS, upgrades the connection, and re-EHLOs.
func doStartTLS(c *textproto.Conn, rawConn net.Conn, tlsCfg *tls.Config, hostname string, verbose bool, authMethods *[]string) error {
	if err := smtpCmd(c, verbose, "STARTTLS", 220); err != nil {
		return fmt.Errorf("STARTTLS: %w", err)
	}
	tlsConn := tls.Client(rawConn, tlsCfg)
	if verbose {
		fmt.Println("[TLS handshake complete — STARTTLS]")
	}
	*c = *textproto.NewConn(tlsConn)
	newMethods, _, _ := ehlo(c, hostname, verbose)
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
