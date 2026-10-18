@echo off
rem 1) icon (assets\maxmix.ico + .png)
rem 2) resource object with the icon (embedded into both binaries)
rem 3) maxmix.exe  - tray application, no console window (double-click / autostart)
rem    maxmixc.exe - same program as a console binary for the command line
go run .\tools\mkicon || exit /b 1
go run .\tools\mksyso assets\maxmix.ico rsrc_windows_amd64.syso || exit /b 1
go build -ldflags "-H=windowsgui -s -w" -o maxmix.exe . || exit /b 1
go build -ldflags "-s -w" -o maxmixc.exe . || exit /b 1
echo built maxmix.exe (tray) and maxmixc.exe (console)
