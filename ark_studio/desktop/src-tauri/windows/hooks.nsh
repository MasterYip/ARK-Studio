; NSIS installer hooks (bundle.windows.nsis.installerHooks). UTF-8 with BOM:
; makensis reads the Chinese text below correctly only with it.
;
; An upgrade or uninstall must not leave the bundled ark-server running from
; the install folder: Windows cannot replace or delete a running .exe, and a
; stale sidecar would keep serving the old version (tauri-apps/tauri#15134).
; The installed app stops only ark-server processes started from its own
; folder (`ark-studio.exe --stop-sidecars`, native, no PowerShell/WMI); a
; standalone ark-server elsewhere on the machine is left alone. The app also
; stops orphaned sidecars at start, and a sidecar exits when the app's stdin
; pipe closes or its job object goes away.

!macro ARK_STOP_SIDECAR
  ${If} ${FileExists} "$INSTDIR\${MAINBINARYNAME}.exe"
    nsExec::Exec '"$INSTDIR\${MAINBINARYNAME}.exe" --stop-sidecars'
    Pop $0
  ${EndIf}
!macroend

!macro NSIS_HOOK_PREINSTALL
  !insertmacro ARK_STOP_SIDECAR
!macroend

; Installing over an existing copy with the installer's default choice
; ("Uninstall before installing") runs the old uninstaller in place
; (`uninstall.exe _?=<install dir>`), while an uninstall started from
; Windows Settings first copies the uninstaller to %TEMP% and runs it from
; there. In place therefore means "part of a reinstall": treat it like an
; update, so the shortcuts, taskbar and Start pins, the autostart entry and
; the app's WebView data stay, even if "Delete app data" was ticked, and the
; uninstaller closes by itself without the "data kept" message below.
!macro NSIS_HOOK_PREUNINSTALL
  ${If} $EXEDIR == $INSTDIR
    StrCpy $UpdateMode 1
  ${EndIf}
  !insertmacro ARK_STOP_SIDECAR
!macroend

; User data next to the app (<install dir>\projects and \data, see
; src/paths.rs) is never deleted: the uninstaller removes only the files it
; installed and the folder only if it is empty. Tell the user where it is.
!macro NSIS_HOOK_POSTUNINSTALL
  ${If} ${FileExists} "$INSTDIR\projects\*.*"
  ${OrIf} ${FileExists} "$INSTDIR\data\*.*"
    DetailPrint "Kept your ARK projects and data in $INSTDIR"
    ; no dialog for silent/passive uninstalls or when an update replaces the app
    ${If} $UpdateMode <> 1
    ${AndIf} $PassiveMode <> 1
    ${AndIfNot} ${Silent}
      ${If} $LANGUAGE = 2052
        MessageBox MB_OK|MB_ICONINFORMATION "你的 ARK 项目和数据保留在：$\r$\n$INSTDIR$\r$\n$\r$\n不再需要时，可自行删除这个文件夹。"
      ${Else}
        MessageBox MB_OK|MB_ICONINFORMATION "Your ARK projects and data were kept in:$\r$\n$INSTDIR$\r$\n$\r$\nDelete that folder yourself if you no longer need them."
      ${EndIf}
    ${EndIf}
  ${EndIf}
!macroend