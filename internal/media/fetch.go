package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// MaxFetchBytes caps a media_url download. WhatsApp's own limits are lower
// for images (16 MB after compression) and higher for documents; 100 MB keeps
// a runaway download from filling the disk.
const MaxFetchBytes = 100 << 20

// Fetcher downloads remote media for sending. The zero value refuses
// loopback, private, link-local and CGNAT (tailnet) addresses, so a public
// MCP caller cannot use it to reach services on this host or network.
type Fetcher struct {
	// AllowPrivate disables the address check (tests only).
	AllowPrivate bool
	Timeout      time.Duration
	MaxBytes     int64
}

var errBlockedAddr = errors.New("media_url resolves to a private or local address")

func blockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	if cgnat.Contains(ip) {
		return true
	}
	_, ula, _ := net.ParseCIDR("fd7a:115c:a1e0::/48") // Tailscale IPv6
	return ula.Contains(ip)
}

func (f Fetcher) client() *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	if !f.AllowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || blockedIP(ip) {
				return errBlockedAddr
			}
			return nil
		}
	}
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to a non-http URL")
			}
			return nil
		},
	}
}

// Fetch downloads rawURL into a new private directory under root and returns
// the file path plus a cleanup func that removes the directory. The file is
// named filename when given, else after the URL path; the extension comes
// from the name, then the Content-Type, then content sniffing, because the
// sender picks image/video/document from the extension.
func (f Fetcher) Fetch(ctx context.Context, rawURL, root, filename string) (string, func(), error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", nil, fmt.Errorf("media_url must be an http(s) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, fmt.Errorf("media_url: %w", err)
	}
	req.Header.Set("User-Agent", "whatsapp-mcp/1 (+media fetch)")
	resp, err := f.client().Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddr) {
			return "", nil, errBlockedAddr
		}
		return "", nil, fmt.Errorf("download media_url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", nil, fmt.Errorf("download media_url: HTTP %d", resp.StatusCode)
	}
	maxBytes := f.MaxBytes
	if maxBytes == 0 {
		maxBytes = MaxFetchBytes
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("download media_url: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return "", nil, fmt.Errorf("media_url is larger than %d MB", maxBytes>>20)
	}
	if len(data) == 0 {
		return "", nil, errors.New("media_url returned an empty body")
	}

	name := fetchName(filename, resp.Request.URL.Path, resp.Header.Get("Content-Type"), data)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", nil, fmt.Errorf("media root: %w", err)
	}
	dir, err := os.MkdirTemp(root, ".fetch-")
	if err != nil {
		return "", nil, fmt.Errorf("media temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write media: %w", err)
	}
	return p, cleanup, nil
}

// fetchName picks a safe file name with a meaningful extension.
func fetchName(filename, urlPath, contentType string, data []byte) string {
	name := sanitizeName(filename)
	if name == "" {
		name = sanitizeName(path.Base(urlPath))
	}
	if name == "" || name == "." || name == "/" {
		name = "file"
	}
	if path.Ext(name) != "" && knownExt(path.Ext(name)) {
		return name
	}
	ct := contentType
	if ct == "" || strings.HasPrefix(ct, "application/octet-stream") || strings.HasPrefix(ct, "binary/") {
		ct = http.DetectContentType(data)
	}
	if ext := extForType(ct); ext != "" {
		return strings.TrimSuffix(name, path.Ext(name)) + ext
	}
	return name
}

func knownExt(ext string) bool {
	return mime.TypeByExtension(strings.ToLower(ext)) != ""
}

var preferredExt = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"video/mp4":       ".mp4",
	"video/quicktime": ".mov",
	"audio/ogg":       ".ogg",
	"audio/mpeg":      ".mp3",
	"application/pdf": ".pdf",
	"text/plain":      ".txt",
}

func extForType(ct string) string {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	if ext, ok := preferredExt[mt]; ok {
		return ext
	}
	if exts, _ := mime.ExtensionsByType(mt); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = path.Base(strings.ReplaceAll(s, "\\", "/"))
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimLeft(s, ".")
	if len(s) > 120 {
		ext := path.Ext(s)
		if len(ext) > 10 {
			ext = ""
		}
		s = s[:120-len(ext)] + ext
	}
	return s
}
