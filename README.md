# gosmtp-cli

A lightweight, zero-dependency SMTP CLI tool written in Go. Send emails from the command line — useful for scripts, monitoring, and automation.

## Install

```bash
go build -o gosmtp-cli .
```

No external dependencies — stdlib only.

## Usage

```
gosmtp-cli -s SERVER -f FROM -t TO [options]
```

### Options

| Flag | Description |
|------|-------------|
| `-s HOST[:PORT]` | SMTP server (host or host:port) |
| `--port N` | SMTP port (default: 25, auto-overridden by host:port) |
| `-f ADDRESS` | Sender address |
| `-t ADDRESS` | Recipient (repeatable, comma-separated) |
| `-cc ADDRESS` | CC recipient (repeatable, comma-separated) |
| `-bcc ADDRESS` | BCC recipient (repeatable, comma-separated) |
| `-u SUBJECT` | Subject line |
| `-m MESSAGE` | Message body (plain text) |
| `-xu USERNAME` | SMTP auth username |
| `-xp PASSWORD` | SMTP auth password |
| `--tls auto\|yes\|no` | STARTTLS mode (default: auto) |
| `--ssl` | Direct TLS on connect (SMTPS / port 465) |
| `-v` | Verbose — print SMTP session |

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

**Multiple recipients:**
```bash
gosmtp-cli -s smtp.example.com:587 -f me@example.com \
  -t alice@example.com -t bob@example.com \
  -cc charlie@example.com \
  -bcc audit@example.com \
  -xu me@example.com -xp mypassword \
  -u "Team update" -m "See attached."
```

**Verbose session output:**
```bash
gosmtp-cli -s smtp.example.com:587 ... -v
```

## Auth methods

Supported (auto-selected by server capability, strongest preferred):
1. CRAM-MD5
2. PLAIN
3. LOGIN

## TLS modes

| Mode | Behavior |
|------|----------|
| `--tls auto` | Use STARTTLS if server offers it (default) |
| `--tls yes` | Require STARTTLS — fail if server doesn't support it |
| `--tls no` | Never use STARTTLS |
| `--ssl` | Direct TLS on connect (port 465 / SMTPS) |

Port 465 automatically implies `--ssl`.
