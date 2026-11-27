//! Whether a remote server answers, checked before it is opened.
//!
//! The main window (WebView2, WKWebView, WebKitGTK) follows the current system
//! proxy settings, which proxy tools such as Clash switch on and off while the
//! app runs. So every check reads the settings afresh (or, with Settings ›
//! Proxy on "direct" or "custom", follows that setting). When a proxy applies to
//! the address, the server is tried through the proxy and directly at the same
//! time; either answer counts. Any HTTP answer means "reachable" (401, 403, an
//! HTML page from a reverse proxy); only connection, TLS and timeout failures
//! count against it, and the reason is passed on to the UI.
//!
//! Certificates are checked against the OS store (rustls-platform-verifier),
//! the same roots the WebView trusts. On Windows that includes an online
//! revocation check of the server certificate, which can take several seconds
//! when the CRL is not cached yet, hence the generous timeout.

use std::time::Duration;

use futures_util::future::{select, Either};
use serde_json::{json, Value};

use crate::proxy::{self, ProxyConfig, Route};

/// Upper bound for one check (both routes run at the same time).
pub const TIMEOUT: Duration = Duration::from_secs(15);
const CONNECT_TIMEOUT: Duration = Duration::from_secs(12);

/// Why a route did not get an HTTP answer.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Reason {
    Certificate,
    Dns,
    Refused,
    Proxy,
    Timeout,
    Network,
}

impl Reason {
    pub fn code(self) -> &'static str {
        match self {
            Reason::Certificate => "certificate",
            Reason::Dns => "dns",
            Reason::Refused => "refused",
            Reason::Proxy => "proxy",
            Reason::Timeout => "timeout",
            Reason::Network => "network",
        }
    }

    /// Order for the one reason shown when every route failed: the most
    /// specific first.
    fn rank(self) -> u8 {
        match self {
            Reason::Certificate => 0,
            Reason::Dns => 1,
            Reason::Refused => 2,
            Reason::Proxy => 3,
            Reason::Timeout => 4,
            Reason::Network => 5,
        }
    }
}

/// One failed attempt: the reason and a short technical detail.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Failure {
    pub reason: Reason,
    pub detail: String,
}

/// The outcome of one route: an HTTP status, or why there was none.
pub type Attempt = Result<u16, Failure>;

/// Maps a request error (its message chain, outermost first) to a reason.
/// `via_proxy`: the attempt went through an HTTP proxy, so a broken tunnel
/// is the proxy's fault rather than the server's.
pub fn classify(chain: &[String], is_timeout: bool, via_proxy: bool) -> Failure {
    let all = chain.join(" | ");
    let lower = all.to_ascii_lowercase();
    let innermost = chain.last().cloned().unwrap_or_default();
    let reason = if is_timeout || lower.contains("timed out") {
        Reason::Timeout
    } else if lower.contains("certificate") || lower.contains("unknownissuer") {
        Reason::Certificate
    } else if via_proxy {
        Reason::Proxy
    } else if lower.contains("dns error") {
        Reason::Dns
    } else if lower.contains("connection refused") || lower.contains("os error 10061") || lower.contains("os error 111)") {
        Reason::Refused
    } else {
        Reason::Network
    };
    let detail = match reason {
        Reason::Certificate => chain
            .iter()
            .find_map(|m| m.strip_prefix("invalid peer certificate: ").map(str::to_string))
            .unwrap_or(innermost),
        _ => innermost,
    };
    Failure { reason, detail }
}

/// Combines the routes tried into the check result.
pub fn verdict(url: &str, proxy: Option<&str>, attempts: &[Attempt]) -> Value {
    if let Some(status) = attempts.iter().find_map(|a| a.as_ref().ok()) {
        return json!({ "url": url, "reachable": true, "ark": (200..300).contains(status), "status": status });
    }
    let worst = attempts.iter().filter_map(|a| a.as_ref().err()).min_by_key(|f| f.reason.rank());
    let (reason, detail) = match worst {
        Some(f) => (f.reason.code(), f.detail.as_str()),
        None => (Reason::Network.code(), ""),
    };
    json!({
        "url": url,
        "reachable": false,
        "ark": false,
        "reason": reason,
        "error": detail,
        "proxy": proxy,
    })
}

/// The unreachable answer of `connect_remote` and friends, with the reason
/// of the check for the UI.
pub fn unreachable(check: &Value, url: &str) -> Value {
    json!({
        "opened": false,
        "unreachable": true,
        "url": url,
        "reason": check["reason"],
        "error": check["error"],
        "proxy": check["proxy"],
    })
}

/// The system proxy for `url` right now, if any (environment variables, then
/// the OS settings incl. the bypass list).
fn system_proxy(url: &reqwest::Url) -> Option<String> {
    let uri: http::Uri = url.as_str().parse().ok()?;
    let hit = hyper_util::client::proxy::matcher::Matcher::from_system().intercept(&uri)?;
    let p = hit.uri();
    Some(match (p.host(), p.port_u16()) {
        (Some(h), Some(port)) => format!("{h}:{port}"),
        (Some(h), None) => h.to_string(),
        _ => p.to_string(),
    })
}

fn client(user_agent: &str, direct: bool, cfg: &ProxyConfig) -> Option<reqwest::Client> {
    let mut b = reqwest::Client::builder()
        .user_agent(user_agent)
        .connect_timeout(CONNECT_TIMEOUT)
        .timeout(TIMEOUT)
        // A redirect is an answer too.
        .redirect(reqwest::redirect::Policy::none());
    if direct {
        b = b.no_proxy();
    } else {
        b = proxy::client_builder(b, cfg);
    }
    b.build().ok()
}

fn error_chain(e: &reqwest::Error) -> Vec<String> {
    let mut out = vec![e.to_string()];
    let mut cur = std::error::Error::source(e);
    while let Some(c) = cur {
        out.push(c.to_string());
        cur = c.source();
    }
    out
}

async fn attempt(client: Option<reqwest::Client>, url: reqwest::Url, via_proxy: bool) -> Attempt {
    let Some(client) = client else {
        return Err(Failure { reason: Reason::Network, detail: "HTTP client unavailable".into() });
    };
    match client.get(url).send().await {
        Ok(r) => Ok(r.status().as_u16()),
        Err(e) => Err(classify(&error_chain(&e), e.is_timeout(), via_proxy)),
    }
}

/// GET <base>/api/health, through the proxy of `cfg` (if one applies) and
/// directly; returns as soon as one route answers.
pub async fn check(base: &reqwest::Url, user_agent: &str, cfg: &ProxyConfig) -> Value {
    let Ok(health) = base.join("api/health") else {
        return verdict(base.as_str(), None, &[]);
    };
    let proxy = match cfg.route() {
        Route::System => system_proxy(&health),
        Route::Direct => None,
        Route::Custom(_) => proxy::custom_proxy_for(cfg, health.as_str()),
    };
    let direct = attempt(client(user_agent, true, cfg), health.clone(), false);
    let Some(proxy) = proxy else {
        return verdict(base.as_str(), None, &[direct.await]);
    };
    let proxied = attempt(client(user_agent, false, cfg), health, true);
    // The first answer wins; after a failure, wait for the other route.
    let attempts = match select(Box::pin(proxied), Box::pin(direct)).await {
        Either::Left((Ok(status), _)) | Either::Right((Ok(status), _)) => vec![Ok(status)],
        Either::Left((proxied, direct)) => vec![proxied, direct.await],
        Either::Right((direct, proxied)) => vec![proxied.await, direct],
    };
    verdict(base.as_str(), Some(&proxy), &attempts)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn chain(parts: &[&str]) -> Vec<String> {
        parts.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn classifies_request_errors() {
        // Messages as reqwest 0.13 / hyper-util report them on Windows.
        let cert = chain(&["error sending request for url (https://x/)", "client error (Connect)", "invalid peer certificate: Expired"]);
        assert_eq!(classify(&cert, false, false), Failure { reason: Reason::Certificate, detail: "Expired".into() });
        let refused = chain(&["error sending request", "client error (Connect)", "tcp connect error", "No connection could be made because the target machine actively refused it. (os error 10061)"]);
        assert_eq!(classify(&refused, false, false).reason, Reason::Refused);
        let refused_unix = chain(&["error sending request", "tcp connect error", "Connection refused (os error 111)"]);
        assert_eq!(classify(&refused_unix, false, false).reason, Reason::Refused);
        let dns = chain(&["error sending request", "client error (Connect)", "dns error", "error resolving DNS", "No such host is known. (os error 11001)"]);
        assert_eq!(classify(&dns, false, false).reason, Reason::Dns);
        let timeout = chain(&["error sending request for url (https://x/)", "operation timed out"]);
        assert_eq!(classify(&timeout, true, false).reason, Reason::Timeout);
        assert_eq!(classify(&timeout, false, true).reason, Reason::Timeout);
        // Through Clash: CONNECT accepted, then the tunnel closes.
        let eof = chain(&["error sending request", "client error (Connect)", "tls handshake eof"]);
        assert_eq!(classify(&eof, false, true), Failure { reason: Reason::Proxy, detail: "tls handshake eof".into() });
        assert_eq!(classify(&eof, false, false).reason, Reason::Network);
        let proxy_down = chain(&["error sending request", "client error (Connect)", "tunnel error: failed to create underlying connection", "tcp connect error", "(os error 10061)"]);
        assert_eq!(classify(&proxy_down, false, true).reason, Reason::Proxy);
        assert_eq!(classify(&[], false, false), Failure { reason: Reason::Network, detail: String::new() });
    }

    fn fail(reason: Reason) -> Attempt {
        Err(Failure { reason, detail: reason.code().into() })
    }

    #[test]
    fn any_http_answer_is_reachable() {
        for status in [200u16, 204, 301, 401, 403, 404, 502] {
            let v = verdict("https://s/", None, &[Ok(status)]);
            assert_eq!(v["reachable"], true, "{status}");
            assert_eq!(v["ark"], (200..300).contains(&status), "{status}");
        }
    }

    #[test]
    fn one_answering_route_is_enough() {
        // Proxy broken, server fine directly (and the other way round).
        let v = verdict("https://s/", Some("127.0.0.1:7890"), &[fail(Reason::Proxy), Ok(200)]);
        assert_eq!(v["reachable"], true);
        assert_eq!(v["ark"], true);
        let v = verdict("https://s/", Some("127.0.0.1:7890"), &[Ok(200), fail(Reason::Timeout)]);
        assert_eq!(v["reachable"], true);
    }

    #[test]
    fn unreachable_reports_the_most_specific_reason() {
        let v = verdict("https://s/", Some("127.0.0.1:7890"), &[fail(Reason::Proxy), fail(Reason::Timeout)]);
        assert_eq!(v["reachable"], false);
        assert_eq!(v["reason"], "proxy");
        assert_eq!(v["proxy"], "127.0.0.1:7890");
        let v = verdict("https://s/", None, &[fail(Reason::Timeout)]);
        assert_eq!(v["reason"], "timeout");
        assert!(v["proxy"].is_null());
        let v = verdict("https://s/", Some("p:1"), &[fail(Reason::Network), fail(Reason::Certificate)]);
        assert_eq!((v["reason"].as_str(), v["error"].as_str()), (Some("certificate"), Some("certificate")));
        let v = verdict("https://s/", None, &[]);
        assert_eq!((v["reachable"].as_bool(), v["reason"].as_str()), (Some(false), Some("network")));
    }

    #[test]
    fn unreachable_answer_carries_the_reason() {
        let check = verdict("https://s/", Some("p:1"), &[fail(Reason::Proxy)]);
        let v = unreachable(&check, "https://s/");
        assert_eq!(v["opened"], false);
        assert_eq!(v["unreachable"], true);
        assert_eq!(v["reason"], "proxy");
        assert_eq!(v["proxy"], "p:1");
    }
}
