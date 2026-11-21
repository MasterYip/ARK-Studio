fn main() {
    // Declaring the app's commands turns on ACL checks for them: a page may
    // only call what a capability grants it (capabilities/bundled.json for
    // the bundled pages; ARK server pages get runtime capabilities, see
    // grant_server_origin in src/lib.rs).
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
            "check_update",
            "open_releases",
            "desktop_context",
            "go_local_home",
            "desktop_servers",
            "server_status",
            "open_server",
            "open_settings",
            "link_server",
            "server_projects",
            "create_server_project",
            "unlink_server",
            "open_local_path",
            "update_state",
            "copy_image_png",
            "device_id",
            "measure_paths",
            "choose_project_folder",
            "tray_menu_ready",
            "tray_menu_action",
        ]),
    ))
    .expect("failed to run tauri-build");
}
