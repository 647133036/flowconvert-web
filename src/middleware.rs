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
            "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; connect-src 'self'; font-src 'self'; frame-ancestors 'none'",
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

/// Mirrors Go's IsPrivate for IpAddr: RFC1918 for IPv4, ULA for IPv6.
fn ip_is_private(ip: &IpAddr) -> bool {
    match ip {
        IpAddr::V4(v4) => v4.is_private(),
        IpAddr::V6(v6) => v6.is_unique_local(),
    }
}
