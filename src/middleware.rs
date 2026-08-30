use std::collections::HashMap;
use std::net::{IpAddr, SocketAddr};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use axum::body::Body;
use axum::extract::{ConnectInfo, Request};
use axum::http::{HeaderName, HeaderValue, Method};
use axum::middleware::Next;
use axum::response::Response;

/// CORS middleware adds security headers (mirrors Go `CORS`).
pub async fn security_headers(req: Request, next: Next) -> Response {
    let mut res = if req.method() == Method::OPTIONS {
        Response::builder()
            .status(200)
            .body(Body::empty())
            .expect("static response")
    } else {
        next.run(req).await
    };

    let headers = [
        ("X-Content-Type-Options", "nosniff"),
        ("X-Frame-Options", "DENY"),
        ("X-XSS-Protection", "1; mode=block"),
        (
            "Content-Security-Policy",
            "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https:; connect-src 'self'; font-src 'self'; frame-ancestors 'none'",
        ),
        ("Access-Control-Allow-Origin", "*"),
        ("Access-Control-Allow-Methods", "GET, POST, OPTIONS"),
        ("Access-Control-Allow-Headers", "Content-Type, Authorization"),
    ];
    for (name, value) in headers {
        if let (Ok(name), Ok(value)) = (
            HeaderName::try_from(name),
            HeaderValue::from_str(value),
        ) {
            res.headers_mut().insert(name, value);
        }
    }
    res
}

/// Caps the total size of API request bodies. It covers the largest
/// legitimate upload (50MB file + multipart field overhead) while preventing
/// oversized bodies from being buffered by the multipart extractor.
pub const MAX_API_BODY: usize = 64 << 20;

struct IpBucket {
    count: usize,
    window_end: Instant,
}

const MAX_BUCKETS: usize = 10000;

#[derive(Debug, PartialEq, Eq)]
pub enum RateDecision {
    Allow,
    TooManyRequests,
    Busy,
}

/// Per-IP sliding-window rate limiter (mirrors Go `RateLimit`).
pub struct RateLimiter {
    buckets: Mutex<HashMap<String, IpBucket>>,
    max_requests: usize,
}

impl RateLimiter {
    pub fn new(max_requests: usize) -> Arc<Self> {
        Arc::new(Self {
            buckets: Mutex::new(HashMap::new()),
            max_requests,
        })
    }

    /// Spawns a background task that evicts expired buckets every 5 minutes.
    pub fn spawn_cleanup(self: &Arc<Self>) {
        let limiter = Arc::clone(self);
        tokio::spawn(async move {
            let mut ticker = tokio::time::interval(Duration::from_secs(5 * 60));
            loop {
                ticker.tick().await;
                let mut buckets = limiter.buckets.lock().unwrap();
                let now = Instant::now();
                buckets.retain(|_, b| now <= b.window_end);
            }
        });
    }

    pub fn check(&self, ip: &str) -> RateDecision {
        let mut buckets = self.buckets.lock().unwrap();
        let now = Instant::now();
        let needs_new = buckets
            .get(ip)
            .is_none_or(|b| now > b.window_end);
        if needs_new {
            if buckets.len() >= MAX_BUCKETS {
                buckets.retain(|_, b| now <= b.window_end);
                if buckets.len() >= MAX_BUCKETS {
                    return RateDecision::Busy;
                }
            }
            buckets.insert(
                ip.to_string(),
                IpBucket {
                    count: 0,
                    window_end: now + Duration::from_secs(60),
                },
            );
        }
        let b = buckets.get_mut(ip).expect("bucket just inserted");
        b.count += 1;
        if b.count <= self.max_requests {
            RateDecision::Allow
        } else {
            RateDecision::TooManyRequests
        }
    }
}

/// Resolves the client identity for rate limiting.
///
/// When the request arrives via a loopback/private peer (reverse proxy on
/// the same host), the X-Forwarded-For chain is inspected. We take the
/// LEFTMOST (original client) entry, but only if it is a valid public IP —
/// private/loopback addresses in XFF are ignored to prevent spoofing.
/// Direct connections use the peer address.
pub fn client_ip(req: &Request) -> String {
    let host = req
        .extensions()
        .get::<ConnectInfo<SocketAddr>>()
        .map(|ci| ci.0.ip().to_string())
        .unwrap_or_else(|| "0.0.0.0".to_string());
    let Ok(peer) = host.parse::<IpAddr>() else {
        return host;
    };
    if !peer.is_loopback() && !ip_is_private(&peer) {
        return host;
    }
    // Behind a proxy; try X-Forwarded-For then X-Real-IP.
    if let Some(fwd) = req
        .headers()
        .get("x-forwarded-for")
        .and_then(|v| v.to_str().ok())
    {
        for part in fwd.split(',') {
            if let Ok(ip) = part.trim().parse::<IpAddr>() {
                if !ip.is_loopback() && !ip_is_private(&ip) && !ip.is_unspecified() {
                    return ip.to_string();
                }
            }
        }
    }
    if let Some(real) = req
        .headers()
        .get("x-real-ip")
        .and_then(|v| v.to_str().ok())
    {
        if let Ok(ip) = real.trim().parse::<IpAddr>() {
            return ip.to_string();
        }
    }
    host
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::http::{HeaderValue, Request};
    use std::net::{IpAddr, SocketAddr};

    fn make_request(peer_ip: &str, xff: Option<&str>, x_real_ip: Option<&str>) -> Request<axum::body::Body> {
        let mut req = Request::builder()
            .uri("/api/test")
            .body(axum::body::Body::empty())
            .unwrap();
        req.extensions_mut().insert(ConnectInfo(SocketAddr::from((
            peer_ip.parse::<IpAddr>().unwrap(),
            12345,
        ))));
        if let Some(v) = xff {
            req.headers_mut().insert("x-forwarded-for", v.parse().unwrap());
        }
        if let Some(v) = x_real_ip {
            req.headers_mut().insert("x-real-ip", v.parse().unwrap());
        }
        req
    }

    #[test]
    fn rate_limiter_allows_within_window() {
        let limiter = RateLimiter::new(100);
        for _ in 0..100 {
            assert_eq!(limiter.check("1.2.3.4"), RateDecision::Allow);
        }
    }

    #[test]
    fn rate_limiter_rejects_over_limit() {
        let limiter = RateLimiter::new(100);
        for _ in 0..100 {
            let _ = limiter.check("1.2.3.4");
        }
        assert_eq!(limiter.check("1.2.3.4"), RateDecision::TooManyRequests);
    }

    #[test]
    fn rate_limiter_max_buckets_returns_busy() {
        let limiter = RateLimiter::new(10);
        // Fill up all buckets
        for i in 0..MAX_BUCKETS {
            let ip = format!("1.{}.{}.{}", (i >> 16) & 0xff, (i >> 8) & 0xff, i & 0xff);
            let _ = limiter.check(&ip);
        }
        // Next distinct IP should hit busy
        let overflow_ip = format!("255.255.255.255");
        assert_eq!(limiter.check(&overflow_ip), RateDecision::Busy);
    }

    #[test]
    fn client_ip_direct_connection() {
        let req = make_request("203.0.113.5", None, None);
        assert_eq!(client_ip(&req), "203.0.113.5");
    }

    #[test]
    fn client_ip_xff_uses_public_ip() {
        let req = make_request("127.0.0.1", Some("198.51.100.10, 10.0.0.1"), None);
        // 127.0.0.1 is loopback, so XFF is consulted; 198.51.100.10 is public
        assert_eq!(client_ip(&req), "198.51.100.10");
    }

    #[test]
    fn client_ip_xff_skips_private_ips() {
        let req = make_request("127.0.0.1", Some("192.168.1.1, 203.0.113.5"), None);
        // 192.168.x is private, so skip to next which is 203.0.113.5
        assert_eq!(client_ip(&req), "203.0.113.5");
    }

    #[test]
    fn client_ip_xff_all_private_falls_back_to_host() {
        let req = make_request("127.0.0.1", Some("192.168.1.1, 10.0.0.1"), None);
        assert_eq!(client_ip(&req), "127.0.0.1");
    }

    #[test]
    fn client_ip_ipv6_loopback() {
        let req = make_request("::1", None, None);
        assert_eq!(client_ip(&req), "::1");
    }

    #[test]
    fn client_ip_ipv6_ula_falls_back_to_xff() {
        let req = make_request("fc00::1", Some("2001:db8::1"), None);
        assert_eq!(client_ip(&req), "2001:db8::1");
    }

    #[tokio::test]
    async fn security_headers_options_returns_200_with_cors() {
        use axum::{routing::get, Router};
        use tower::ServiceExt;

        async fn dummy_handler() -> &'static str { "ok" }
        let app = Router::new()
            .route("/test", get(dummy_handler))
            .layer(axum::middleware::from_fn(security_headers));

        let req = Request::builder()
            .method("OPTIONS")
            .uri("/test")
            .body(axum::body::Body::empty())
            .unwrap();
        let res = app.oneshot(req).await.unwrap();
        assert_eq!(res.status(), 200);
        let headers = res.headers();
        assert_eq!(headers.get("Access-Control-Allow-Origin").unwrap(), "*");
        assert_eq!(headers.get("Access-Control-Allow-Methods").unwrap(), "GET, POST, OPTIONS");
        assert_eq!(headers.get("X-Content-Type-Options").unwrap(), "nosniff");
        assert_eq!(headers.get("X-Frame-Options").unwrap(), "DENY");
    }

    #[tokio::test]
    async fn security_headers_normal_request_contains_security_headers() {
        use axum::{routing::get, Router};
        use tower::ServiceExt;

        async fn dummy_handler() -> &'static str { "ok" }
        let app = Router::new()
            .route("/test", get(dummy_handler))
            .layer(axum::middleware::from_fn(security_headers));

        let req = Request::builder()
            .method("GET")
            .uri("/test")
            .body(axum::body::Body::empty())
            .unwrap();
        let res = app.oneshot(req).await.unwrap();
        assert_eq!(res.status(), 200);
        let headers = res.headers();
        assert_eq!(headers.get("X-Content-Type-Options").unwrap(), "nosniff");
        assert_eq!(headers.get("X-Frame-Options").unwrap(), "DENY");
        assert_eq!(headers.get("X-XSS-Protection").unwrap(), "1; mode=block");
        let csp = headers.get("Content-Security-Policy").unwrap().to_str().unwrap();
        assert!(csp.contains("script-src 'self'"));
        assert!(!csp.contains("'unsafe-inline'"));
    }
}

/// Mirrors Go's IsPrivate for IpAddr: RFC1918 for IPv4, ULA for IPv6.
fn ip_is_private(ip: &IpAddr) -> bool {
    match ip {
        IpAddr::V4(v4) => v4.is_private(),
        IpAddr::V6(v6) => v6.is_unique_local(),
    }
}
