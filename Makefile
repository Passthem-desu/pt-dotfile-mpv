.PHONY: install pcutter pcutter.exe

install:
	python ./tools/generate.py

# 本机版本（Linux/WSL 上需要 GTK3 + WSLg）
pcutter:
	cd tools/pcutter && go build -o pcutter .

# Windows 版本，给 mpv 的 input.conf 用
pcutter.exe:
	cd tools/pcutter && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
		go build -ldflags "-H windowsgui -s -w" -o pcutter.exe .
