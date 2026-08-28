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
	"syscall"
	"time"
)

var (
	ErrBadURL    = errors.New("无效或不允许访问的 URL")
	ErrTooLarge  = errors.New("文件超过 20MB 限制")
	ErrNotImage  = errors.New("URL 内容不是有效图片")
)

func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
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
	}
	return false
}

func checkHost(host string) error {
	h := host
	if host, _, err := net.SplitHostPort(host); err == nil {
		h = host
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

// ssrfSafeDialer returns a dialer whose Control callback validates the
// resolved IP at connection time, closing the DNS-rebinding window between
// hostname validation and the actual TCP connect.
func ssrfSafeDialer() *net.Dialer {
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return ErrBadURL
			}
			if isBlockedIP(ip) {
				return ErrBadURL
			}
			return nil
		},
	}
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
		Timeout:   30 * time.Second,
		Transport: &http.Transport{DialContext: ssrfSafeDialer().DialContext},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("重定向次数过多")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return ErrBadURL
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
	if resp.ContentLength > 0 && resp.ContentLength > maxBytes {
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