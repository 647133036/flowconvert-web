package service

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrBadURL    = errors.New("无效或不允许访问的 URL")
	ErrTooLarge  = errors.New("文件超过 20MB 限制")
	ErrNotImage  = errors.New("URL 内容不是有效图片")
)

// isPrivateIP reports whether ip is loopback, private, link-local or
// an unspecified/reserved address that can't be safely fetched.
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	// Also block common internal ranges like 169.254, CGNAT 100.64/10, 192.0.0.0/24
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
		if ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0 {
			return true
		}
		if ip4[0] == 10 || ip4[0] == 172 && (ip4[1] >= 16 && ip4[1] <= 31) || ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
	}
	return false
}

func checkHost(host string) error {
	// strip port
	h := host
	if parsed := strings.Split(host, ":"); len(parsed) > 0 {
		h = parsed[0]
	}
	ips, err := net.LookupIP(h)
	if err != nil {
		return fmt.Errorf("域名解析失败: %w", err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return ErrBadURL
		}
	}
	return nil
}

// FetchImage downloads an image URL into tmpDir, enforcing SSRF + size + MIME constraints.
func FetchImage(tmpDir, rawURL string, maxBytes int64) (string, error) {
	if rawURL == "" {
		return "", ErrBadURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", ErrBadURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrBadURL
	}
	if err := checkHost(u.Hostname()); err != nil {
		return "", err
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("重定向次数过多")
			}
			return checkHost(req.URL.Hostname())
		},
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		return "", ErrNotImage
	}
	if resp.ContentLength > maxBytes && resp.ContentLength > 0 {
		return "", ErrTooLarge
	}
	if maxBytes <= 0 {
		maxBytes = 20 << 20
	}

	tmpName := "url_" + NewID(10)
	tmpPath := filepath.Join(tmpDir, tmpName)
	f, err := os.Create(tmpPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	written, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("下载失败: %w", err)
	}
	if written > maxBytes {
		_ = os.Remove(tmpPath)
		return "", ErrTooLarge
	}
	return tmpPath, nil
}