; The installer owns this handle from confirmation through the end of uninstall.
; The sharing flags match internal/system/installation_windows.go: readers and deletion are allowed,
; new writers (including a second installer) are not. The handle is closed automatically on exit.
Var RayDockReservation

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
    MessageBox MB_OK|MB_ICONSTOP "无法完成 RayDock 的退出、代理恢复或自启清理，卸载已停止。原因记录在 $INSTDIR\data\uninstall-error.txt。请确认 UAC、退出程序后重试。" /SD IDOK
    Abort
  done:
FunctionEnd

Function un.onInit
  ; Confirm once before resource cleanup, not after changing the running application's state.
  IfSilent prepare
  MessageBox MB_OKCANCEL|MB_ICONQUESTION "卸载将先退出 RayDock、恢复系统代理并移除开机自启，并删除程序目录及其中的 data 文件夹（节点、订阅与设置）。需要保留请先备份 data 文件夹。是否继续？" IDOK prepare
  SetErrorLevel 1
  Abort
  prepare:
    System::Call 'kernel32::CreateFileW(w "$INSTDIR\.raydock-uninstall.lock", i 0x40000000, i 5, p 0, i 4, i 0x00200080, p 0) p .r9'
    StrCmp $9 -1 leaseFailed
    StrCpy $RayDockReservation $9
    Call un.PrepareRayDock
    ; Our confirmation replaces the stock confirmation page, whose cleanup would otherwise happen later.
    SetSilent silent
    Return
  leaseFailed:
    SetErrorLevel 1
    MessageBox MB_OK|MB_ICONSTOP "无法独占 RayDock 安装目录，卸载已停止。" /SD IDOK
    Abort
FunctionEnd
