chcp 65001 >nul
setlocal
title Evernight Realm
echo Evernight Realm

if not defined PROXY_URL   set "PROXY_URL=http://192.168.255.1:23334"
if not defined HTTPS_PROXY set "HTTPS_PROXY=%PROXY_URL%"
if not defined HTTP_PROXY  set "HTTP_PROXY=%PROXY_URL%"
if not defined NO_PROXY    set "NO_PROXY=127.0.0.1,localhost"

echo chk...

where go >nul 2>nul
if errorlevel 1 (
  echo ERR NO GO
  exit /b 1
)
where flutter >nul 2>nul
if errorlevel 1 (
  echo ERR NO FLUTTER
  exit /b 1
)

set "FRONTEND_DEVICE=windows"
if /i "%~1"=="web" set "FRONTEND_DEVICE=chrome"

start "evernight-server (backend)" cmd /k "title evernight-server (backend) && cd /d %~dp0 && go run -v ./cmd/evernight-server"

start "evernight-realm-app (frontend)" cmd /k "title evernight-realm-app (frontend) && cd /d %~dp0evernight-realm-app && flutter run -v -d %FRONTEND_DEVICE%"

timeout 3
exit /b 0
