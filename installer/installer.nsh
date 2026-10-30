; Prepare before MyGo removes shortcuts/registration or falls back to terminating a tray process.
; RayDock asks its existing instance to quit, waits for its cleanup, recovers a stale owned proxy and
; removes only this installation's autostart entries. Every failure prevents file deletion.
Function un.PrepareRayDock
  ClearErrors
  ExecWait '"$INSTDIR\RayDock.exe" --prepare-uninstall' $0
  IfErrors failed
  StrCmp $0 0 done
  failed:
    SetErrorLevel 1
    MessageBox MB_OK|MB_ICONSTOP "无法完成 RayDock 的退出、代理恢复或自启清理，卸载已停止。请确认 UAC、退出程序后重试。" /SD IDOK
    Abort
  done:
FunctionEnd

Function un.onInit
  ; Confirm once before resource cleanup, not after changing the running application's state.
  IfSilent prepare
  MessageBox MB_OKCANCEL|MB_ICONQUESTION "卸载将先退出 RayDock、恢复系统代理并移除开机自启。是否继续？" IDOK prepare
  SetErrorLevel 1
  Abort
  prepare:
    Call un.PrepareRayDock
    ; Our confirmation replaces the stock confirmation page, whose cleanup would otherwise happen later.
    SetSilent silent
FunctionEnd

!macro customUnInstall
  ; Recheck immediately before removing files, including recovery after a last-moment process exit.
  Call un.PrepareRayDock
!macroend
