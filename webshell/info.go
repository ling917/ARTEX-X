package webshell

import (
	"context"
	"regexp"
	"strings"
)

// Info fills the basic target information panel in a single round trip. Each
// field is delimited by a marker line so the parsing stays independent of the
// shell's own banner or error output.
func (c *Client) Info(ctx context.Context) (*InfoResult, error) {
	if err := c.EnsureOS(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	osType := c.osType
	start := nowMS()
	cmd := unixInfoCommand()
	if osType == "windows" {
		cmd = windowsInfoCommand()
	}
	res, err := c.executeLocked(ctx, cmd)
	if err != nil {
		return nil, err
	}
	sections := parseSections(res.Output)
	out := &InfoResult{
		OSType:     osType,
		User:       firstLine(sections["__WS_USER__"]),
		Hostname:   firstLine(sections["__WS_HOST__"]),
		OS:         strings.TrimSpace(sections["__WS_OS__"]),
		Pwd:        firstLine(sections["__WS_PWD__"]),
		IP:         firstIP(sections["__WS_IP__"]),
		Raw:        trimForDisplay(res.Output),
		Transport:  c.transport,
		DurationMS: nowMS() - start,
	}
	return out, nil
}

func unixInfoCommand() string {
	return `printf '__WS_USER__\n'; (id 2>/dev/null || whoami 2>/dev/null)
printf '__WS_HOST__\n'; (hostname 2>/dev/null)
printf '__WS_OS__\n'; (uname -a 2>/dev/null; cat /etc/os-release 2>/dev/null | head -n 6)
printf '__WS_PWD__\n'; (pwd 2>/dev/null)
printf '__WS_IP__\n'; (ip -o -4 addr show 2>/dev/null || ifconfig 2>/dev/null) | head -n 40`
}

func windowsInfoCommand() string {
	script := `Write-Output '__WS_USER__'
Write-Output (('{0}\{1}' -f $env:USERDOMAIN,$env:USERNAME))
Write-Output '__WS_HOST__'
Write-Output $env:COMPUTERNAME
Write-Output '__WS_OS__'
try { (Get-CimInstance Win32_OperatingSystem).Caption } catch { [Environment]::OSVersion.VersionString }
Write-Output '__WS_PWD__'
Write-Output (Get-Location).Path
Write-Output '__WS_IP__'
try { Get-NetIPAddress -AddressFamily IPv4 | ForEach-Object { $_.IPAddress } } catch { ipconfig | Select-String 'IPv4' }`
	return powershellCommand(script)
}

// parseSections splits marker-delimited output. A line is treated as a marker
// only when the whole line is a __WS_...__ token.
func parseSections(raw string) map[string]string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	out := map[string]string{}
	current := ""
	var buf []string
	flush := func() {
		if current != "" {
			out[current] = strings.TrimSpace(strings.Join(buf, "\n"))
		}
		buf = nil
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "__WS_") && strings.HasSuffix(trimmed, "__") && !strings.Contains(trimmed, " ") {
			flush()
			current = trimmed
			continue
		}
		if current != "" {
			buf = append(buf, line)
		}
	}
	flush()
	return out
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if v := strings.TrimSpace(line); v != "" {
			return v
		}
	}
	return ""
}

var ipv4Pattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

func firstIP(s string) string {
	all := ipv4Pattern.FindAllString(s, -1)
	for _, ip := range all {
		if ip == "127.0.0.1" || ip == "0.0.0.0" || strings.HasPrefix(ip, "255.") {
			continue
		}
		return ip
	}
	if len(all) > 0 {
		return all[0]
	}
	return ""
}
