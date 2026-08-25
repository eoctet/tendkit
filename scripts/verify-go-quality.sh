#!/usr/bin/env bash

set -euo pipefail

readonly STATICCHECK_VERSION="v0.8.1"
readonly GOVULNCHECK_VERSION="v1.7.0"
readonly GOSEC_VERSION="v2.28.0"

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repository_root"

missing=0
require_command() {
	local command_name="$1"
	local install_command="${2:-}"
	if command -v "$command_name" >/dev/null 2>&1; then
		return
	fi
	printf '缺少命令：%s\n' "$command_name" >&2
	if [[ -n "$install_command" ]]; then
		printf '安装命令：%s\n' "$install_command" >&2
	fi
	missing=1
}

require_command go
require_command git
require_command python3
require_command staticcheck "go install honnef.co/go/tools/cmd/staticcheck@${STATICCHECK_VERSION}"
require_command govulncheck "go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"
require_command gosec "GOPROXY=direct go install github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION}"

if ((missing != 0)); then
	exit 127
fi

require_module_version() {
	local command_name="$1"
	local module_path="$2"
	local expected_version="$3"
	local install_command="$4"
	local actual_version
	actual_version="$(go version -m "$(command -v "$command_name")" | awk -v module="$module_path" '$1 == "mod" && $2 == module { print $3; exit }')"
	if [[ "$actual_version" == "$expected_version" ]]; then
		return
	fi
	printf '%s 版本不匹配：期望 %s，实际 %s\n' "$command_name" "$expected_version" "${actual_version:-unknown}" >&2
	printf '安装命令：%s\n' "$install_command" >&2
	exit 1
}

require_module_version staticcheck honnef.co/go/tools "$STATICCHECK_VERSION" "go install honnef.co/go/tools/cmd/staticcheck@${STATICCHECK_VERSION}"
require_module_version govulncheck golang.org/x/vuln "$GOVULNCHECK_VERSION" "go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"
require_module_version gosec github.com/securego/gosec/v2 "$GOSEC_VERSION" "GOPROXY=direct go install github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION}"

unformatted="$(gofmt -l cmd internal pkg)"
if [[ -n "$unformatted" ]]; then
	printf '以下 Go 文件需要 gofmt：\n%s\n' "$unformatted" >&2
	exit 1
fi

go test ./...
go test -race ./...
go vet ./...
go build ./...
staticcheck ./...
govulncheck ./...
gosec -quiet ./...

python3 -m json.tool internal/config/template/default_config.json >/dev/null
python3 -m json.tool pkg/i18n/locales/zh.json >/dev/null
python3 -m json.tool pkg/i18n/locales/en.json >/dev/null
git diff --check
git diff --cached --check
