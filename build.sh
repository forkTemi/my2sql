#!/bin/bash

# build.sh - Go 项目多平台编译脚本
# 支持：Linux/amd64, macOS/amd64, macOS/arm64

set -e  # 遇到错误立即退出

# 获取项目名称（默认为当前目录名）
PROJECT_NAME="${1:-$(basename "$(pwd)")}"
echo "开始编译项目: $PROJECT_NAME"

# 输出目录
OUTPUT_DIR="dist"
rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

# 编译版本信息（可选：从 git 获取）
VERSION=$(git describe --tags 2>/dev/null || echo "unknown")
TIMESTAMP=$(date -u '+%Y-%m-%d %H:%M:%S UTC')

# 编译函数
build() {
    local os="$1"
    local arch="$2"
    local ext="$3"  # 文件后缀，如 "" 或 ".exe"
    local output_name="${PROJECT_NAME}-${os}-${arch}${ext}"
    local output_path="${OUTPUT_DIR}/${output_name}"

    echo "编译: ${os}/${arch} -> ${output_name}"

    GOOS=$os GOARCH=$arch \
    CGO_ENABLED=0 \
    go build \
        -ldflags "-s -w -X 'main.version=${VERSION}' -X 'main.buildTime=${TIMESTAMP}'" \
        -o "$output_path" \
        .

    # 可选：显示文件大小
    echo "  生成: $(du -h "$output_path" | cut -f1)"
}

# 开始编译

# Linux amd64
build linux amd64 ""

# macOS amd64 (Intel)
build darwin amd64 ""

# macOS arm64 (Apple Silicon M1/M2/M3)
build darwin arm64 ""

echo "✅ 所有平台编译完成！"
echo "输出目录: ./$OUTPUT_DIR/"
ls -lh "$OUTPUT_DIR/"
