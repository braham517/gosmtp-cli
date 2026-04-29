# gosmtp-cli

A lightweight, zero-dependency SMTP CLI tool written in Go. Send emails from the command line — useful for scripts, monitoring, and automation.

## Install

**Download a pre-built binary** (Linux, macOS, Windows — no Go required):

```bash
# macOS (Apple Silicon)
curl -L https://github.com/braham517/gosmtp-cli/releases/latest/download/gosmtp-cli_darwin_arm64.tar.gz | tar xz
sudo mv gosmtp-cli /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/braham517/gosmtp-cli/releases/latest/download/gosmtp-cli_darwin_amd64.tar.gz | tar xz
sudo mv gosmtp-cli /usr/local/bin/

# Linux (amd64)
curl -L https://github.com/braham517/gosmtp-cli/releases/latest/download/gosmtp-cli_linux_amd64.tar.gz | tar xz
sudo mv gosmtp-cli /usr/local/bin/
```

Or grab the binary directly from the [Releases page](https://github.com/braham517/gosmtp-cli/releases).

**Go install** (requires Go 1.21+):

```bash
go install github.com/braham517/gosmtp-cli@latest
```

**Build from source:**

```bash
git clone https://github.com/braham517/gosmtp-cli.git
cd gosmtp-cli
go build -o gosmtp-cli .
```

No external dependencies — stdlib only.

## Usage

```
gosmtp-cli -s SERVER -f FROM -t TO [options]
```

### Options

**Connection**

| Flag | Description |
|------|-------------|
| `-s HOST[:PORT]` | SMTP server (host or host:port); omit to resolve via MX lookup |
| `-port N` | SMTP port (default: 25, overridden by host:port in `-s`) |
| `-tls auto\|yes\|no` | STARTTLS mode (default: auto) |
| `-ssl` | Direct TLS on connect (SMTPS / port 465) |
| `-timeout N` | Connection timeout in seconds (default: 30) |
| `-no-verify-cert` | Skip TLS certificate verification |

**Sender & Recipients**

| Flag | Description |
|------|-------------|
| `-f ADDRESS` | Sender address |
| `-t ADDRESS` | Recipient (repeatable, comma-separated) |
| `-cc ADDRESS` | CC recipient (repeatable, comma-separated) |
| `-bcc ADDRESS` | BCC recipient (repeatable, comma-separated) |

**Message**

| Flag | Description |
|------|-------------|
| `-u SUBJECT` | Subject line |
| `-m TEXT\|FILE\|-` | Plain text body (literal, file path, or `-` for stdin) |
| `-body-html TEXT\|FILE\|-` | HTML body (literal, file path, or `-` for stdin) |
| `-a FILE` | Attach a file (repeatable) |
| `-add-header "Name: value"` | Add a custom header (repeatable) |
| `-replace-header "Name: value"` | Replace a built-in header (repeatable) |

**Authentication**

| Flag | Description |
|------|-------------|
| `-xu USERNAME` | SMTP auth username |
| `-xp PASSWORD` | SMTP auth password |

**Output**

| Flag | Description |
|------|-------------|
| `-v` | Verbose — print full SMTP session |

## Examples

**Simple open relay:**
```bash
gosmtp-cli -s mail.example.com -f sender@example.com -t recipient@example.com \
  -u "Hello" -m "Test message"
```

**With SMTP auth over STARTTLS (port 587):**
```bash
gosmtp-cli -s smtp.example.com:587 -f me@example.com -t you@example.com \
  -xu me@example.com -xp mypassword \
  -u "Hello" -m "Test message"
```

**Direct TLS (port 465 / SMTPS):**
```bash
gosmtp-cli -s smtp.example.com:465 -f me@example.com -t you@example.com \
  -xu me@example.com -xp mypassword \
  -u "Hello" -m "Test message"
```

**HTML email:**
```bash
gosmtp-cli -s smtp.example.com:465 -f me@example.com -t you@example.com \
  -xu me@example.com -xp mypassword \
  -u "Hello" -body-html "<h1>Hello!</h1><p>This is an HTML email.</p>"
```

**Plain + HTML (multipart/alternative):**
```bash
gosmtp-cli -s smtp.example.com:465 -f me@example.com -t you@example.com \
  -xu me@example.com -xp mypassword \
  -u "Hello" -m "Hello!" -body-html "<h1>Hello!</h1>"
```

**With attachments:**
```bash
gosmtp-cli -s smtp.example.com:465 -f me@example.com -t you@example.com \
  -xu me@example.com -xp mypassword \
  -u "Report" -m "See attached." \
  -a report.pdf -a logs.zip
```

**Body from file:**
```bash
gosmtp-cli -s smtp.example.com:465 -f me@example.com -t you@example.com \
  -xu me@example.com -xp mypassword \
  -u "Newsletter" -body-html email.html
```

**Body from stdin:**
```bash
echo "Hello from a script" | gosmtp-cli -s smtp.example.com:465 \
  -f me@example.com -t you@example.com \
  -xu me@example.com -xp mypassword \
  -u "Hello" -m -
```

**Multiple recipients:**
```bash
gosmtp-cli -s smtp.example.com:587 -f me@example.com \
  -t alice@example.com -t bob@example.com \
  -cc charlie@example.com \
  -bcc audit@example.com \
  -xu me@example.com -xp mypassword \
  -u "Team update" -m "See attached." -a report.pdf
```

**MX lookup (no -s needed):**
```bash
gosmtp-cli -f me@example.com -t you@gmail.com \
  -u "Hello" -m "Test"
```

**Skip cert verification (dev/internal servers):**
```bash
gosmtp-cli -s mail.internal:465 -f me@example.com -t you@example.com \
  -no-verify-cert -u "Test" -m "Test"
```

## MIME structure

The message format is chosen automatically based on the flags used:

| Flags used | MIME type |
|------------|-----------|
| `-m` only | `text/plain` |
| `-body-html` only | `text/html` |
| `-m` + `-body-html` | `multipart/alternative` |
| Any of the above + `-a` | `multipart/mixed` |

## Auth methods

Supported (auto-selected by server capability, strongest preferred):

1. CRAM-MD5
2. PLAIN
3. LOGIN

## TLS modes

| Flag | Behavior |
|------|----------|
| `-tls auto` | Use STARTTLS if server offers it (default) |
| `-tls yes` | Require STARTTLS — fail if server doesn't support it |
| `-tls no` | Never use STARTTLS |
| `-ssl` | Direct TLS on connect (port 465 / SMTPS) |

Port 465 automatically implies `-ssl`.
