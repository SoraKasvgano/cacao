# Git 提交与 PR 约定

一个 PR 只解决一个能单独说明、验证和回滚的行为目标。一个 PR 可以包含多个相关 commit；同一功能的前后端、测试和必要文档可以一起提交。无关重构、依赖升级、格式化或另一个功能应拆成其他 PR。

## 从干净的基线开始

`origin` 指向自己的 fork，`upstream` 指向 `https://github.com/lanthora/cacao.git`。先用 `git remote -v` 检查；缺少上游时执行 `git remote add upstream https://github.com/lanthora/cacao.git`。

```sh
git fetch upstream
git switch --no-track -c fix/session-expiry upstream/master
```

分支使用 `feat/目标`、`fix/目标`、`refactor/目标`、`docs/目标`、`chore/目标` 等简短名称。不要从含有未合并功能的 fork `master` 开始新的独立功能。并行工作可用 `git worktree add --no-track -b feat/device-status ../cacao-device-status upstream/master`。

只在当前仓库设置以下选项（不要加 `--global`）：

```sh
git config --local pull.ff only
git config --local rerere.enabled true
git config --local remote.pushDefault origin
git config --local push.default simple
git config --local commit.template .gitmessage
```

## 提交与提交前检查

commit 首行和 PR 标题使用 `type(scope): 具体变化`，例如 `fix(auth): 过期会话返回未登录状态`。`scope` 可省略，破坏兼容性时使用 `!`，例如 `feat(api)!: 移除旧登录接口`。类型为 `feat`、`fix`、`docs`、`style`、`refactor`、`perf`、`test`、`build`、`ci`、`chore`、`revert`；scope 使用小写字母、数字和 `._/-`。首行不超过 100 个字符，正文解释原因和重要取舍。

```sh
git status --short
git diff
git add -p
git diff --cached --check
git diff --cached
git commit
```

每个 commit 应是有意义的工作单元。提交前删除调试输出、凭据和无关变动。合并前整理临时 `fixup!` / `squash!` 提交；这些标题不通过校验。Git 自动生成的 merge commit 不校验标题；日常同步优先 rebase。

按变化验证：Go 逻辑运行相关包测试，涉及公共逻辑时运行 `go test ./...`；先构建前端生成 Go embed 所需资源。前端运行 `npm --prefix frontend run build`，有对应测试脚本时再运行它。新环境先在 `frontend` 安装依赖。文档检查链接和命令；流程脚本运行 `node --test scripts/git-policy.test.mjs`。按范围运行现有 `make` / `make all`，在 PR 写出实际命令、结果及未覆盖项，不把没有运行的检查标为通过。

可选本地 commit-msg hook 需要 Node.js 22+（Git for Windows 自带所需 sh）：

```sh
git config --show-origin --get core.hooksPath
# 仅在确认不会覆盖现有 hook 配置后执行：
git config --local core.hooksPath .githooks
```

hook 检查首行；CI 再检查 PR 标题和该 PR 新增的非 merge commit。hook 可被跳过，不能替代 CI。撤销本次设置使用 `git config --local --unset core.hooksPath`；原先有本地值时恢复原值。worktree 共享仓库配置，旧分支可能还没有这些文件，启用前确认工作分支。

## PR 范围与依赖

提交前核对真正的 PR 基线，例如：

```sh
git fetch upstream
git log --oneline upstream/master..HEAD
git diff --stat upstream/master...HEAD
git diff upstream/master...HEAD
node scripts/git-policy.mjs pr upstream/master HEAD "fix(auth): 过期会话返回未登录状态"
```

`...` 从共同祖先计算改动，避免把基线后来新增的内容误算进 PR。PR 模板必须说明唯一目标、范围、依赖和验证。建议不超过 400 行非生成内容增删、15 个文件；超出时说明为什么仍是一个目标，并提供阅读顺序。锁文件和 `frontend/dist` 单独计数；总文件数仍包含它们。体积只提醒，不能判断功能相关性，小 PR 也可能混入无关变化。

依赖其他功能时，先从父功能分支创建子分支，记录父分支当时的 commit SHA，并明确 PR 依赖关系。GitHub PR 的 base 分支必须存在于目标仓库：若上游没有父功能分支，可在自己的 fork 内展示父子差异；正式上游 PR 应按依赖顺序提交，等父 PR 合并后再更新子分支。不要将整条依赖链作为一个上游 PR。

父 PR 被 squash merge 后，先确保工作区干净，再把子分支中父分支之后的提交移到新的上游基线：

```sh
git fetch upstream
git switch feat/child
git branch backup/child-before-rebase
git rebase --onto upstream/master OLD_PARENT_SHA
```

将 `OLD_PARENT_SHA` 替换成子分支建立时记录的父分支尖端（若后来吸收了父分支更新，则用子分支实际包含的最终父尖端）。解决冲突后重新验证 diff 和测试。对已经发布、由自己独占的分支，协调后用 `git push --force-with-lease origin feat/child` 更新；未发布分支无需强推。不要重写共享 `master` 或他人的分支。

合并方式建议 squash merge，最终标题遵循同一格式；相关多 commit 仍可用于评审，也可按维护者约定保留。仓库维护者可启用分支保护：通过 `Check / build` 和 `PR policy / policy`、至少一位 reviewer 批准、处理未解决讨论后才能合并，并禁止受保护分支强推。这些是建议，仓库文件不会自动配置 GitHub 保护规则。
