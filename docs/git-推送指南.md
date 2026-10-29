# Git 推送与创建 PR 指南

面向本项目（`llm-gateway`）的实操记录，记录把本地代码推到 GitHub 并开 Pull Request 的完整流程。

## 一、一次性配置（每个仓库做一次）

```bash
# 1) 关联远程仓库：origin 是远程地址的别名
git remote add origin https://github.com/ai07863/llm_gateway.git

# 2) 确认关联成功（应看到 fetch / push 两条地址）
git remote -v

# 3) 若访问 GitHub 需要代理（例如本地 7890 端口），配置全局代理
git config --global http.proxy http://127.0.0.1:7890
git config --global https.proxy http://127.0.0.1:7890
```

> 注：若命令在仓库目录外执行，可用 `git -C "仓库路径" ...` 指定仓库。

## 二、每次推送

```bash
# 推送 main 分支，并设置跟踪关系
git push -u origin main

# 推送功能分支，并设置跟踪关系
git push -u origin feat/llm-gateway-impl
```

| 片段 | 含义 |
|---|---|
| `git push` | 把本地提交上传到远程 |
| `origin` | 远程仓库别名 |
| `main` / `feat/...` | 要推送的分支名 |
| `-u` / `--set-upstream` | 设置跟踪关系；以后直接 `git push` / `git pull` 即可，无需再写 `origin 分支名` |

首次推送后，日常开发只需：

```bash
git add .
git commit -m "说明"
git push
```

## 三、创建 Pull Request

`git push` 后，GitHub 会在终端回显创建链接，例如：

```
remote: Create a pull request for 'feat/llm-gateway-impl' on GitHub by visiting:
remote:   https://github.com/ai07863/llm_gateway/pull/new/feat/llm-gateway-impl
```

点击该链接 → **base** 选 `main`、**compare** 选功能分支 → 填写标题与描述 → Create。

## 四、常用排查

```bash
git remote -v          # 查看远程地址
git branch -vv         # 查看本地分支跟踪的远端分支
git status             # 查看改动
git log --oneline -5   # 查看最近提交
```

## 五、取消代理

```bash
git config --global --unset http.proxy
git config --global --unset https.proxy
```

## 常见问题

- **`Connection was reset` / 连不上 github.com**：多为网络限制，配置代理后重试；可用
  `Test-NetConnection github.com -Port 443 -InformationLevel Quiet` 检查连通性。
- **推送要求登录**：需配置 Personal Access Token 或 Git Credential Manager。
- **远程已有提交导致推送被拒（non-fast-forward）**：先 `git pull --rebase` 再推；不要贸然 force-push。