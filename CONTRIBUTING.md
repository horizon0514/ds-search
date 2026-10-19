# 参与贡献 / Contributing

欢迎 issue 和 PR！

- **开发环境**：Go 1.27+。`go tool mygo dev` 在 macOS 上运行（热重载）；在其他系统上也能跑测试、交叉编译 macOS 版。
- **提交前**：`gofmt -w .`、`go vet ./...`、`go test -short ./...` 都要通过（CI 会跑同样的命令）。改了界面的话，附一张浅色和深色的截图。
- **真实 API 测试**：设置 `DEEPSEEK_API_KEY` 后 `go test ./...` 会调用真实 API（产生少量费用）并把截图渲染到 `shots/`。不设置时自动跳过。
- **代码约定**：UI 用 MyGo 的 `ui` 包；状态在 `model.go`，界面在 `view.go` / `settings.go` / `chatview.go`，DeepSeek 客户端在 `dsapi/`，计费在 `billing/`。界面文字目前是中文。
- **安全**：永远不要提交 API Key、`.env` 或 `api-key` 文件，也不要把 Key 写进日志或错误信息。发现安全问题请私下联系维护者，不要公开 issue。
- 提交即表示你同意以 MIT 许可证发布你的贡献。

English: PRs welcome. Run `gofmt`, `go vet ./...` and `go test -short ./...` before submitting; never commit API keys. Contributions are MIT-licensed.
