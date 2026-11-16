fn main() {
    // Declaring the app's commands turns on ACL checks for them: a page may
    // only call what a capability grants it (capabilities/chooser.json for
    // the bundled pages; remote ARK pages get "allow-open-local-path" only).
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(
        tauri_build::AppManifest::new().commands(&[
            "get_state",
            "pick_folder",
            "connect_local",
            "connect_remote",
            "save_settings",
            "forget_remote",
            "open_data_dir",
            "open_log",
            "stop_local",
            "close_window",
            "open_chooser",
            "check_update",
            "open_local_path",
        ]),
    ))
    .expect("failed to run tauri-build");
}
