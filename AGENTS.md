# skills — 开发与测试 SOP

所有 Agent-native 技能和文档入口，请参阅 [README.md](README.md) 中的 `## Agent-native Skills` 章节。

## 本地测试

```bash
go vet ./...
go test -race -count=1 ./...
```

测试覆盖 `install.go`、`manifest.go`、`main.go`。不要 mock，走真实文件系统。

## 调试与安装辨析

**绝对禁止混淆安装版本与调试版本**：
- **开发调试**：直接本地编译（如 `go build -o ./skills .`），并运行本地绝对/相对路径二进制（如 `./skills`），绝不执行 `make install` 或拷贝至 `~/.local/bin`。
- **日常使用**：统一使用包管理器（Homebrew `skills-cli`）安装的版本。系统 `PATH` 维持 `brew` 优先级在前。

## 对 dotfiles 回归

dotfiles 是主要测试目标。每次改完行为必须：

```bash
git -C ~/.dotfiles checkout -- .       # 恢复到干净状态
go build -o /tmp/skills-dev .          # 本地编译
/tmp/skills-dev install -q             # 安装所有技能
git -C ~/.dotfiles diff                # 只应有实际变更，不能有格式噪音
/tmp/skills-dev fmt -q                 # 格式化，幂等
/tmp/skills-dev remove --dry-run xxx   # dry-run 不应写任何文件
```

只用 `git checkout` 恢复 dotfiles，禁止手动重写被 skills 改过的文件。

## 发布流程

1. `git commit` + `git tag vX.Y.Z` + `git push --tags`
2. 等 Release workflow 完成（含 update-homebrew）
3. `brew update && brew upgrade skills-cli` 两端
4. `ssh mac "brew update && brew upgrade skills-cli"`

## CI 注意事项

- `update-homebrew` 依赖 `GH_TOKEN` 环境变量推送到 homebrew-tap。格式：`gh repo clone` + `gh auth setup-git` + `git push`
- Python 脚本 `update-homebrew-formula.py` 用正则修改 formula，注意缩进和匹配模式
- 不要为了修 CI 错误刷版本号。amend 或删 tag 重打
