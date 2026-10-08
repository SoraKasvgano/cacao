@echo off
setlocal
cd /d "%~dp0"

echo [1/5] Building frontend...
pushd frontend
call npm run build
if errorlevel 1 (
    echo npm run build failed, trying npm install first...
    call npm install
    if errorlevel 1 (
        echo npm install failed
        popd
        exit /b 1
    )
    call npm run build
    if errorlevel 1 (
        echo frontend build failed
        popd
        exit /b 1
    )
)
popd

if not exist dist mkdir dist

set CGO_ENABLED=0

echo [2/5] Building dist\cacao-linux-amd64...
set GOOS=linux
set GOARCH=amd64
go build -trimpath -ldflags "-w -s" -o dist\cacao-linux-amd64 . || goto :fail

echo [3/5] Building dist\cacao-linux-arm64...
set GOOS=linux
set GOARCH=arm64
go build -trimpath -ldflags "-w -s" -o dist\cacao-linux-arm64 . || goto :fail

echo [4/5] Building dist\cacao-linux-armv7...
set GOOS=linux
set GOARCH=arm
set GOARM=7
go build -trimpath -ldflags "-w -s" -o dist\cacao-linux-armv7 . || goto :fail

echo [5/5] Building dist\cacao-windows-amd64...
set GOOS=windows
set GOARCH=amd64
set GOARM=
go build -trimpath -ldflags "-w -s" -o dist\cacao-windows-amd64 . || goto :fail

echo.
echo Build OK:
dir /b dist\cacao-*
exit /b 0

:fail
echo Build failed
exit /b 1
