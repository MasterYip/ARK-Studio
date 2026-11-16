// Prevents an additional console window on Windows in release builds.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    // Used by the installer hooks (windows/hooks.nsh): stop ark-server
    // processes running from this app's folder, then exit.
    if std::env::args().any(|a| a == "--stop-sidecars") {
        ark_studio_lib::stop_installed_sidecars();
        return;
    }
    ark_studio_lib::run()
}
