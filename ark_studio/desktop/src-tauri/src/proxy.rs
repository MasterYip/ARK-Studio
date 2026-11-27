//! Settings › Proxy: how the app's own connections reach the network.
//!
//! Three modes, stored in `desktop.json` (`proxy`):
//!
//! * `system` (default): the OS / environment proxy, as before;
//! * `direct`: no proxy for anything;
//! * `custom`: one http://, https:// or socks5:// proxy plus an optional
//!   list of hosts that connect directly.
//!
//! The loopback names (`noproxy::LOOPBACK`) bypass the proxy in every mode:
//! the main window talks to its own sidecar on 127.0.0.1.
//!
//! Where the setting applies:
//!
//! * the webview: WebView2 browser arguments (`webview2_args`, fixed when
//!   the first window is created, so a change needs a restart); WebKitGTK
//!   network proxy settings of the shared website data manager
//!   (`apply_webview`, applied at once); WKWebView keeps the system settings;
//! * Rust HTTP clients and the updater (`client_builder`, `updater`): read the
//!   current setting for every request;
//! * the sidecar: `HTTP_PROXY` / `HTTPS_PROXY` / `ALL_PROXY` / `NO_PROXY`
//!   (`env_plan`) when it starts;
//! * this process: the same variables once at start (`apply_env`), for
//!   anything that reads the environment.

use std::collections::HashMap;
use std::sync::OnceLock;

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::noproxy;

pub const SYSTEM: &str = "system";
pub const DIRECT: &str = "direct";
pub const CUSTOM: &str = "custom";
const SCHEMES: &[&str] = &["http", "https", "socks5"];
const BYPASS_MAX: usize = 64;
/// wry's own WebView2 arguments; passing ours replaces them, so they are kept.
pub const WEBVIEW2_DEFAULT_ARGS: &str = "--disable-features=msWebOOUI,msPdfOOUI,msSmartScreenProtection";
pub const PROXY_KEYS: [&str; 6] = ["http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY"];
pub const NO_PROXY_KEYS: [&str; 2] = ["no_proxy", "NO_PROXY"];

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ProxyConfig {
    /// `system`, `direct` or `custom`; anything else reads as `system`.
    pub mode: String,
    /// The custom proxy (normalized: `scheme://host:port`).
    pub url: String,
    /// Hosts that connect directly in custom mode (besides the loopback names).
    pub bypass: Vec<String>,
}

impl Default for ProxyConfig {
    fn default() -> ProxyConfig {
        ProxyConfig { mode: SYSTEM.into(), url: String::new(), bypass: Vec::new() }
    }
}

/// What a connection does under a `ProxyConfig`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Route {
    System,
    Direct,
    Custom(String),
}

impl ProxyConfig {
    pub fn route(&self) -> Route {
        match self.mode.as_str() {
            DIRECT => Route::Direct,
            // A hand-edited file with a broken address falls back to the default.
            CUSTOM => normalize_url(&self.url).map(Route::Custom).unwrap_or(Route::System),
            _ => Route::System,
        }
    }

    /// The loopback names, then the user's entries (custom mode only).
    pub fn bypass_hosts(&self) -> Vec<String> {
        let mut out: Vec<String> = noproxy::LOOPBACK.iter().map(|s| s.to_string()).collect();
        if matches!(self.route(), Route::Custom(_)) {
            for entry in &self.bypass {
                let e = if entry == "[::1]" { "::1" } else { entry.as_str() };
                if !out.iter().any(|o| o.eq_ignore_ascii_case(e)) {
                    out.push(e.to_string());
                }
            }
        }
        out
    }

    /// The `no_proxy` list for custom mode.
    pub fn no_proxy_list(&self) -> String {
        self.bypass_hosts().join(",")
    }

    /// Settings › Proxy as sent by the page: `{mode, url, bypass}` (bypass:
    /// text or a list). Custom mode needs a valid address; the other modes
    /// keep a valid address and list for later.
    pub fn from_value(v: &Value) -> Result<ProxyConfig, String> {
        let mode = v.get("mode").and_then(Value::as_str).unwrap_or(SYSTEM).trim();
        if ![SYSTEM, DIRECT, CUSTOM].contains(&mode) {
            return Err(format!("Unknown proxy mode: {mode}"));
        }
        let raw_url = v.get("url").and_then(Value::as_str).unwrap_or("");
        let bypass_text = match v.get("bypass") {
            Some(Value::Array(list)) => list.iter().filter_map(Value::as_str).collect::<Vec<_>>().join(","),
            Some(Value::String(s)) => s.clone(),
            _ => String::new(),
        };
        let custom = mode == CUSTOM;
        let url = match normalize_url(raw_url) {
            Ok(u) => u,
            Err(e) if custom => return Err(e),
            Err(_) => String::new(),
        };
        let bypass = match parse_bypass(&bypass_text) {
            Ok(list) => list,
            Err(e) if custom => return Err(e),
            Err(_) => Vec::new(),
        };
        Ok(ProxyConfig { mode: mode.into(), url, bypass })
    }
}

/// A typed proxy address as `scheme://host:port` (http:// when no scheme
/// is given). No user name, password or path.
pub fn normalize_url(raw: &str) -> Result<String, String> {
    let text = raw.trim();
    if text.is_empty() {
        return Err("Enter the proxy address, for example http://127.0.0.1:7890.".into());
    }
    let with_scheme = if text.contains("://") { text.to_string() } else { format!("http://{text}") };
    let url = url::Url::parse(&with_scheme).map_err(|_| format!("Not a valid proxy address: {text}"))?;
    let scheme = url.scheme().to_ascii_lowercase();
    if !SCHEMES.contains(&scheme.as_str()) {
        return Err("Use an http://, https:// or socks5:// proxy address.".into());
    }
    let host = url.host_str().unwrap_or("").to_ascii_lowercase();
    if host.is_empty() {
        return Err("The proxy address has no host.".into());
    }
    if !url.username().is_empty() || url.password().is_some() {
        return Err("Proxy addresses with a user name or password are not supported.".into());
    }
    if !(url.path().is_empty() || url.path() == "/") || url.query().is_some() || url.fragment().is_some() {
        return Err("Enter only the scheme, host and port of the proxy, for example socks5://127.0.0.1:1080.".into());
    }
    let port = match url.port_or_known_default() {
        Some(p) => p,
        None => return Err("Add the proxy's port, for example socks5://127.0.0.1:1080.".into()),
    };
    Ok(format!("{scheme}://{host}:{port}"))
}

/// The direct-connection list as typed (commas, semicolons, spaces or new
/// lines between entries): host names (`example.com` also covers its
/// subdomains; `.example.com` and `*.example.com` are the same), IP
/// addresses and ranges (`192.168.0.0/16`).
pub fn parse_bypass(raw: &str) -> Result<Vec<String>, String> {
    let mut out: Vec<String> = Vec::new();
    for item in raw.split(|c: char| c == ',' || c == ';' || c.is_whitespace()) {
        let item = item.trim().to_ascii_lowercase();
        if item.is_empty() {
            continue;
        }
        let entry = match item.strip_prefix("*.") {
            Some(rest) => format!(".{rest}"),
            None => item.clone(),
        };
        let body = entry.strip_prefix('.').unwrap_or(&entry);
        let ok = !body.is_empty()
            && body.len() <= 253
            && !body.starts_with('.')
            && body.chars().all(|c| c.is_ascii_alphanumeric() || "-._:/[]".contains(c));
        if !ok {
            return Err(format!("Not a host name, IP address or range: {item}"));
        }
        if !out.contains(&entry) {
            out.push(entry);
        }
    }
    if out.len() > BYPASS_MAX {
        return Err(format!("Enter at most {BYPASS_MAX} entries."));
    }
    Ok(out)
}

// ---------------------------------------------------------------- environment

/// The proxy variables as the app inherited them (before `apply_env`).
static INHERITED: OnceLock<HashMap<&'static str, String>> = OnceLock::new();
/// The setting this run started with (WebView2 arguments, the "restart" note).
static STARTUP: OnceLock<ProxyConfig> = OnceLock::new();

fn inherited(key: &str) -> Option<String> {
    match INHERITED.get() {
        Some(map) => map.get(key).cloned(),
        None => std::env::var(key).ok(),
    }
}

/// Values of the proxy variables for a process under `cfg`: `Some` to set,
/// `None` to remove. `original` reads a variable as the app inherited it.
pub fn env_plan(cfg: &ProxyConfig, original: &dyn Fn(&str) -> Option<String>) -> Vec<(&'static str, Option<String>)> {
    let mut out = Vec::new();
    let merged = |key: &str| {
        let current = original(key);
        noproxy::merged(current.as_deref()).or(current)
    };
    match cfg.route() {
        Route::System => {
            out.extend(PROXY_KEYS.iter().map(|&k| (k, original(k))));
            out.extend(NO_PROXY_KEYS.iter().map(|&k| (k, merged(k))));
        }
        Route::Direct => {
            out.extend(PROXY_KEYS.iter().map(|&k| (k, None)));
            out.extend(NO_PROXY_KEYS.iter().map(|&k| (k, merged(k))));
        }
        Route::Custom(url) => {
            out.extend(PROXY_KEYS.iter().map(|&k| (k, Some(url.clone()))));
            let list = cfg.no_proxy_list();
            out.extend(NO_PROXY_KEYS.iter().map(|&k| (k, Some(list.clone()))));
        }
    }
    out
}

/// The sidecar's proxy variables under the current setting.
pub fn sidecar_env(cfg: &ProxyConfig) -> Vec<(&'static str, Option<String>)> {
    env_plan(cfg, &inherited)
}

/// Applies `cfg` to this process's environment (inherited by the WebView's
/// network process and anything else that reads it) and remembers the
/// setting this run started with. Call first thing in `run()`, before any
/// thread or window exists.
pub fn apply_env(cfg: &ProxyConfig) {
    let _ = INHERITED.set(
        PROXY_KEYS
            .iter()
            .chain(NO_PROXY_KEYS.iter())
            .filter_map(|&k| std::env::var(k).ok().map(|v| (k, v)))
            .collect(),
    );
    let _ = STARTUP.set(cfg.clone());
    for (key, value) in env_plan(cfg, &inherited) {
        match value {
            Some(v) => std::env::set_var(key, v),
            None => std::env::remove_var(key),
        }
    }
}

/// The setting this run started with.
pub fn startup() -> ProxyConfig {
    STARTUP.get().cloned().unwrap_or_default()
}

// ---------------------------------------------------------------- clients

/// A reqwest client builder that follows `cfg` (system: reqwest's own
/// environment and OS lookup).
pub fn client_builder(builder: reqwest::ClientBuilder, cfg: &ProxyConfig) -> reqwest::ClientBuilder {
    match cfg.route() {
        Route::System => builder,
        Route::Direct => builder.no_proxy(),
        Route::Custom(url) => match reqwest::Proxy::all(url.as_str()) {
            Ok(p) => builder.proxy(p.no_proxy(reqwest::NoProxy::from_string(&cfg.no_proxy_list()))),
            Err(_) => builder,
        },
    }
}

/// The proxy (`host:port`) a request to `url` goes through under a custom
/// setting, None when it connects directly.
pub fn custom_proxy_for(cfg: &ProxyConfig, url: &str) -> Option<String> {
    let Route::Custom(proxy) = cfg.route() else { return None };
    let uri: http::Uri = url.parse().ok()?;
    let matcher = hyper_util::client::proxy::matcher::Matcher::builder().all(proxy).no(cfg.no_proxy_list()).build();
    let hit = matcher.intercept(&uri)?;
    let p = hit.uri();
    Some(match (p.host(), p.port_u16()) {
        (Some(h), Some(port)) => format!("{h}:{port}"),
        _ => p.to_string(),
    })
}

// ---------------------------------------------------------------- webviews

/// One entry of Chromium's bypass list; host names also cover subdomains,
/// as they do in `no_proxy`.
fn chromium_bypass(entry: &str) -> Vec<String> {
    if entry.contains('/') || entry.parse::<std::net::IpAddr>().is_ok() || entry.starts_with('[') {
        if entry.parse::<std::net::Ipv6Addr>().is_ok() {
            return vec![format!("[{entry}]")];
        }
        return vec![entry.to_string()];
    }
    let domain = entry.trim_start_matches('.');
    vec![domain.to_string(), format!("*.{domain}")]
}

/// WebView2 browser arguments for `cfg`; None keeps wry's defaults (system).
pub fn webview2_args(cfg: &ProxyConfig) -> Option<String> {
    match cfg.route() {
        Route::System => None,
        Route::Direct => Some(format!("{WEBVIEW2_DEFAULT_ARGS} --no-proxy-server")),
        Route::Custom(url) => {
            let mut list: Vec<String> = Vec::new();
            for entry in cfg.bypass_hosts() {
                for item in chromium_bypass(&entry) {
                    if !list.contains(&item) {
                        list.push(item);
                    }
                }
            }
            Some(format!("{WEBVIEW2_DEFAULT_ARGS} --proxy-server={url} --proxy-bypass-list={}", list.join(";")))
        }
    }
}

/// WebView2 arguments of this run: every window of one WebView2 environment
/// must be created with the same ones, so a change waits for a restart.
#[cfg_attr(not(windows), allow(dead_code))]
pub fn webview2_startup_args() -> Option<String> {
    webview2_args(&startup())
}

/// WebKitGTK: sets the proxy of the website data manager that all windows
/// share. Takes effect for new connections at once.
#[cfg(target_os = "linux")]
pub fn apply_webview(w: &tauri::WebviewWindow, cfg: &ProxyConfig) {
    let route = cfg.route();
    let hosts = cfg.bypass_hosts();
    let result = w.with_webview(move |pw| {
        use webkit2gtk::{NetworkProxyMode, NetworkProxySettings, WebViewExt, WebsiteDataManagerExt};
        let Some(manager) = pw.inner().website_data_manager() else { return };
        match route {
            Route::System => manager.set_network_proxy_settings(NetworkProxyMode::Default, None),
            Route::Direct => manager.set_network_proxy_settings(NetworkProxyMode::NoProxy, None),
            Route::Custom(url) => {
                let ignore: Vec<&str> = hosts.iter().map(String::as_str).collect();
                let mut settings = NetworkProxySettings::new(Some(url.as_str()), &ignore);
                manager.set_network_proxy_settings(NetworkProxyMode::Custom, Some(&mut settings));
            }
        }
    });
    if let Err(e) = result {
        eprintln!("ark-studio: proxy: {e}");
    }
}

#[cfg(not(target_os = "linux"))]
pub fn apply_webview(_w: &tauri::WebviewWindow, _cfg: &ProxyConfig) {}

/// Whether the platform's webview follows a change without a restart.
pub fn webview_live() -> bool {
    cfg!(target_os = "linux")
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn custom(url: &str, bypass: &[&str]) -> ProxyConfig {
        ProxyConfig { mode: CUSTOM.into(), url: url.into(), bypass: bypass.iter().map(|s| s.to_string()).collect() }
    }

    #[test]
    fn normalizes_and_validates_proxy_addresses() {
        assert_eq!(normalize_url("http://127.0.0.1:7890").unwrap(), "http://127.0.0.1:7890");
        assert_eq!(normalize_url(" 127.0.0.1:7890 ").unwrap(), "http://127.0.0.1:7890");
        assert_eq!(normalize_url("SOCKS5://127.0.0.1:1080/").unwrap(), "socks5://127.0.0.1:1080");
        assert_eq!(normalize_url("https://Proxy.Example.com").unwrap(), "https://proxy.example.com:443");
        assert_eq!(normalize_url("http://[::1]:8080").unwrap(), "http://[::1]:8080");
        assert!(normalize_url("").is_err());
        assert!(normalize_url("ftp://127.0.0.1:21").is_err());
        assert!(normalize_url("socks4://127.0.0.1:1080").is_err());
        assert!(normalize_url("socks5://127.0.0.1").is_err(), "socks needs a port");
        assert!(normalize_url("http://user:pw@127.0.0.1:7890").is_err());
        assert!(normalize_url("http://127.0.0.1:7890/path").is_err());
        assert!(normalize_url("http://127.0.0.1:7890?x=1").is_err());
        assert!(normalize_url("http://").is_err());
        assert!(normalize_url("http://127.0.0.1:99999").is_err());
    }

    #[test]
    fn parses_the_bypass_list() {
        assert_eq!(
            parse_bypass(" 192.168.0.0/16, *.Corp.example.com;intranet\n.lan  10.0.0.1 ").unwrap(),
            vec!["192.168.0.0/16", ".corp.example.com", "intranet", ".lan", "10.0.0.1"]
        );
        assert_eq!(parse_bypass("a.example, a.example").unwrap(), vec!["a.example"]);
        assert!(parse_bypass("").unwrap().is_empty());
        assert!(parse_bypass("ok.example, bad!host").is_err());
        assert!(parse_bypass("*").is_err());
        assert!(parse_bypass("..x").is_err());
        let many = (0..65).map(|i| format!("h{i}")).collect::<Vec<_>>().join(",");
        assert!(parse_bypass(&many).is_err());
    }

    #[test]
    fn reads_the_mode_from_a_patch() {
        let c = ProxyConfig::from_value(&json!({"mode": "custom", "url": "127.0.0.1:7890", "bypass": "a.example, 10.0.0.0/8"})).unwrap();
        assert_eq!(c, custom("http://127.0.0.1:7890", &["a.example", "10.0.0.0/8"]));
        let c = ProxyConfig::from_value(&json!({"mode": "custom", "url": "socks5://h:1", "bypass": ["x.example"]})).unwrap();
        assert_eq!(c.route(), Route::Custom("socks5://h:1".into()));
        assert!(ProxyConfig::from_value(&json!({"mode": "custom", "url": ""})).is_err());
        assert!(ProxyConfig::from_value(&json!({"mode": "custom", "url": "h:1", "bypass": "bad host!"})).is_err());
        assert!(ProxyConfig::from_value(&json!({"mode": "vpn"})).is_err());
        // other modes keep what is valid and never fail on the custom fields
        let c = ProxyConfig::from_value(&json!({"mode": "direct", "url": "nonsense://", "bypass": "ok.example"})).unwrap();
        assert_eq!(c, ProxyConfig { mode: DIRECT.into(), url: String::new(), bypass: vec!["ok.example".into()] });
        assert_eq!(ProxyConfig::from_value(&json!({})).unwrap(), ProxyConfig::default());
    }

    #[test]
    fn routes_default_to_system() {
        assert_eq!(ProxyConfig::default().route(), Route::System);
        assert_eq!(ProxyConfig { mode: "other".into(), ..Default::default() }.route(), Route::System);
        assert_eq!(ProxyConfig { mode: DIRECT.into(), ..Default::default() }.route(), Route::Direct);
        // a hand-edited broken address
        assert_eq!(custom("ftp://x", &[]).route(), Route::System);
    }

    #[test]
    fn loopback_is_always_bypassed() {
        for cfg in [ProxyConfig::default(), ProxyConfig { mode: DIRECT.into(), ..Default::default() }, custom("http://p:1", &["[::1]", "LOCALHOST", "x.example"])] {
            let hosts = cfg.bypass_hosts();
            assert_eq!(&hosts[..3], &["localhost", "127.0.0.1", "::1"]);
            let plan = env_plan(&cfg, &|_| None);
            for key in NO_PROXY_KEYS {
                let v = plan.iter().find(|(k, _)| *k == key).and_then(|(_, v)| v.clone()).unwrap();
                for name in noproxy::LOOPBACK {
                    assert!(v.split(',').any(|s| s == *name), "{key}={v}");
                }
            }
        }
        assert_eq!(custom("http://p:1", &["[::1]", "LOCALHOST", "x.example"]).no_proxy_list(), "localhost,127.0.0.1,::1,x.example");
        // the user's list only counts in custom mode
        let direct = ProxyConfig { mode: DIRECT.into(), url: String::new(), bypass: vec!["x.example".into()] };
        assert_eq!(direct.bypass_hosts().len(), 3);
    }

    fn get(plan: &[(&'static str, Option<String>)], key: &str) -> Option<String> {
        plan.iter().find(|(k, _)| *k == key).unwrap().1.clone()
    }

    #[test]
    fn builds_the_environment_per_mode() {
        let inherited = |k: &str| match k {
            "HTTPS_PROXY" => Some("http://127.0.0.1:7890".to_string()),
            "no_proxy" => Some(".corp.example".to_string()),
            _ => None,
        };
        // system: the inherited proxy, loopback added to no_proxy
        let plan = env_plan(&ProxyConfig::default(), &inherited);
        assert_eq!(get(&plan, "HTTPS_PROXY").as_deref(), Some("http://127.0.0.1:7890"));
        assert_eq!(get(&plan, "http_proxy"), None);
        assert_eq!(get(&plan, "no_proxy").as_deref(), Some(".corp.example,localhost,127.0.0.1,::1"));
        assert_eq!(get(&plan, "NO_PROXY").as_deref(), Some("localhost,127.0.0.1,::1"));
        // direct: every proxy variable removed
        let plan = env_plan(&ProxyConfig { mode: DIRECT.into(), ..Default::default() }, &inherited);
        for key in PROXY_KEYS {
            assert_eq!(get(&plan, key), None, "{key}");
        }
        // custom: all six set to the proxy, no_proxy is loopback plus the list
        let plan = env_plan(&custom("socks5://127.0.0.1:1080", &["10.0.0.0/8"]), &inherited);
        for key in PROXY_KEYS {
            assert_eq!(get(&plan, key).as_deref(), Some("socks5://127.0.0.1:1080"), "{key}");
        }
        assert_eq!(get(&plan, "NO_PROXY").as_deref(), Some("localhost,127.0.0.1,::1,10.0.0.0/8"));
        assert_eq!(plan.len(), 8);
    }

    #[test]
    fn builds_webview2_arguments_per_mode() {
        assert_eq!(webview2_args(&ProxyConfig::default()), None);
        assert_eq!(
            webview2_args(&ProxyConfig { mode: DIRECT.into(), ..Default::default() }).unwrap(),
            format!("{WEBVIEW2_DEFAULT_ARGS} --no-proxy-server")
        );
        let args = webview2_args(&custom("http://127.0.0.1:7890", &[".corp.example", "10.0.0.0/8", "fe80::1"])).unwrap();
        assert_eq!(
            args,
            format!(
                "{WEBVIEW2_DEFAULT_ARGS} --proxy-server=http://127.0.0.1:7890 --proxy-bypass-list=localhost;*.localhost;127.0.0.1;[::1];corp.example;*.corp.example;10.0.0.0/8;[fe80::1]"
            )
        );
        assert!(args.starts_with("--disable-features=msWebOOUI,msPdfOOUI,msSmartScreenProtection "));
    }

    #[test]
    fn custom_proxy_applies_except_to_bypassed_hosts() {
        let cfg = custom("socks5://127.0.0.1:1080", &[".corp.example", "10.0.0.0/8"]);
        assert_eq!(custom_proxy_for(&cfg, "https://ark.example.com/api/health").as_deref(), Some("127.0.0.1:1080"));
        assert_eq!(custom_proxy_for(&cfg, "https://a.corp.example/api/health"), None);
        assert_eq!(custom_proxy_for(&cfg, "http://10.1.2.3:8890/api/health"), None);
        assert_eq!(custom_proxy_for(&cfg, "http://127.0.0.1:18890/"), None);
        assert_eq!(custom_proxy_for(&cfg, "http://localhost:18890/"), None);
        assert_eq!(custom_proxy_for(&ProxyConfig::default(), "https://ark.example.com/"), None);
    }

    #[test]
    fn client_builders_build_in_every_mode() {
        let _ = rustls::crypto::ring::default_provider().install_default();
        for cfg in [ProxyConfig::default(), ProxyConfig { mode: DIRECT.into(), ..Default::default() }, custom("socks5://127.0.0.1:1080", &["x.example"]), custom("https://127.0.0.1:8443", &[])] {
            assert!(client_builder(reqwest::Client::builder(), &cfg).build().is_ok(), "{cfg:?}");
        }
    }

    #[test]
    fn stored_settings_round_trip() {
        let cfg = custom("http://127.0.0.1:7890", &["x.example"]);
        let text = serde_json::to_string(&cfg).unwrap();
        assert_eq!(text, r#"{"mode":"custom","url":"http://127.0.0.1:7890","bypass":["x.example"]}"#);
        assert_eq!(serde_json::from_str::<ProxyConfig>(&text).unwrap(), cfg);
        assert_eq!(serde_json::from_str::<ProxyConfig>("{}").unwrap(), ProxyConfig::default());
    }
}
