---
title: "s10 · 影子 Git 检查点"
chapter: 10
slug: s10-checkpoints-shadow-git
est_read_min: 14
---

# s10 · 影子 Git 检查点

> 教什么：**影子 git 检查点**——cline 如何维护一个隐藏的第二 git 仓库，它的工作树就是你的工作区，于是它能在每一步后拍快照、撤销任何改动，却从不碰你真实的 git 历史。这是收官机制，放在最后，因为它把持久的"逐步撤销"包裹在循环所做的一切之外。

---

## Problem

到 s09 为止，智能体已经"危险"得恰到好处：它从真实 provider 流式读取（s05）、实时解析工具调用（s02）、经过审批门（s04）通过注册表分发（s03）、应用外科手术式的文件编辑（s07），甚至从外部 MCP server 加载工具（s09）。这里每一个工具调用都可能改动磁盘上的文件。一次多回合的运行可能在十个回合里改写十几个文件——然后用户想把第 7 回合要回来。

你没法用用户自己的 git 来解决。每一步都提交会把他们真实的历史埋在几百个 `checkpoint-...` 提交之下；用 stash 会和他们的工作区改动打架；而且很多工作区根本不是 git 仓库。你也不能只是把文件拷到备份文件夹——那样你得重新发明 diff、历史、以及"恢复到第 N 个点"。痛点很明确：智能体需要一份**完整的、可恢复的、逐步的工作区时间线，且对项目真实的版本控制完全不可见**。

## Solution

cline 的答案是**影子 git**：一个给工作区拍快照、但历史存在用户永远看不到的地方的第二 git 仓库。整个戏法就是把 git 两个位置 flag 做一次反转：

```
git --git-dir=<shadowDir> --work-tree=<workspace> <子命令>
```

`--git-dir` 是对象库、refs、HEAD 所在的地方；`--work-tree` 是这个仓库追踪哪些文件。把 git 目录指向一个隐藏目录、把工作树指向用户的工作区，你就得到一个仓库：它给用户的文件拍快照，却把所有提交写到*别处*。用户真实的 `.git` 从不被创建、也从不被触碰。

三个设计决策撑起本章：

1. **独立的 `--git-dir` 就是全部的隔离机制。** 在工作区内部跑 `git init` 会创建 `workspace/.git`，开始污染（或撞上）用户的历史。把 git 目录放在外面，让影子和用户的仓库在同一批文件上共存，彼此互不可见。
2. **快照 = `git add -A` + `git commit --allow-empty`。** 每个回合暂存所有改动并提交，返回一个 hash，Task 把它记成一个 `Checkpoint`。`--allow-empty` 让撤销时间线在某个回合啥都没改时也和回合保持对齐。
3. **撤销 = `git reset --hard <hash>`。** 硬重置同时移动 HEAD *和*覆盖工作树——于是用户磁盘上的真实文件回弹到那个检查点。恢复对时间线是非破坏性的：之后你可以继续提交，链条从恢复点继续往下走。

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  每次调用：  git --git-dir=<shadow> --work-tree=<workspace>     │
│                                                                  │
│   Init    ─▶ git init · config 身份 · add -A · commit          │
│   Commit  ─▶ git add -A · git commit --allow-empty ─▶ HASH      │
│   Restore ─▶ git reset --hard HASH ─▶ 覆盖工作区文件            │
│   List    ─▶ git log --format=%H ─▶ [hash, hash, ...]           │
│                                                                  │
│   影子 .git（隐藏）              工作区（用户真实文件）          │
│   ┌──────────────┐   快照        ┌────────────────────────┐    │
│   │ objects/refs │◀───────────────│  app.go  util.go  ...   │    │
│   │ HEAD         │──reset──files─▶│  （这里永远没有 .git）  │    │
│   └──────────────┘                └────────────────────────┘    │
└────────────────────────────────────────────────────────────────┘
```

核心约 45 行节选（出自 [`agents/s10-checkpoints-shadow-git/checkpoint.go`](https://github.com/Ding-Ye/learn-cline/blob/main/agents/s10-checkpoints-shadow-git/checkpoint.go)）——init、每回合的 commit、撤销，以及那个让影子成为影子的唯一 helper：

```go
// Init 创建影子仓库：git 目录在工作区之外，工作树 = 工作区。
func (s *ShadowGit) Init(workdir, shadowDir string) error {
	s.workdir = workdir
	s.shadowDir = shadowDir // 不是 workdir/.git——这正是要点
	if _, err := s.git("init"); err != nil {
		return fmt.Errorf("shadow git init: %w", err)
	}
	for _, kv := range [][2]string{
		{"user.name", "Cline Checkpoint"}, {"user.email", "checkpoint@cline.bot"},
		{"commit.gpgSign", "false"}, // 在裸 CI 机器上提交也必须成功
	} {
		if _, err := s.git("config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("shadow git config %s: %w", kv[0], err)
		}
	}
	if _, err := s.git("add", "-A"); err != nil { // 暂存当前工作区
		return fmt.Errorf("shadow git initial add: %w", err)
	}
	// --allow-empty：必须永远有一个 HEAD 可供 reset 回去。
	_, err := s.git("commit", "--allow-empty", "--no-verify", "-m", "initial commit")
	return err
}

// Commit 给工作区拍快照并返回新 hash（一个 checkpoint）。
func (s *ShadowGit) Commit(message string) (string, error) {
	if _, err := s.git("add", "-A"); err != nil { // 新增 + 修改 + 删除
		return "", err
	}
	if _, err := s.git("commit", "--allow-empty", "--no-verify", "-m", message); err != nil {
		return "", err
	}
	out, err := s.git("rev-parse", "HEAD") // 读回刚写入的 hash
	return strings.TrimSpace(out), err
}

// Restore 把工作区文件回退到更早的检查点（撤销）。
func (s *ShadowGit) Restore(hash string) error {
	_, err := s.git("reset", "--hard", hash) // 同时移动 HEAD 与工作树
	return err
}

// git 在每一次调用前都加上那两个让它成为影子 git 的 flag。
func (s *ShadowGit) git(args ...string) (string, error) {
	full := append([]string{
		"--git-dir=" + s.shadowDir,   // 隐藏的对象库
		"--work-tree=" + s.workdir,   // 用户的工作区
	}, args...)
	cmd := exec.Command("git", full...)
	// ... 捕获 stdout/stderr，把 stderr 放进 error ...
}
```

**四个非显然之处**：

1. **这两个 flag 必须出现在*每一次*调用里。** `git` helper 是唯一的咽喉点，保证 git 目录永远是影子——不存在任何会意外写进 `workspace/.git` 的代码路径。上游则是把 `core.worktree` 一次性写进影子配置，让普通 `git` 自动解析工作树；同一机制，是"配置一次"对"每次传参"。
2. **`--allow-empty` 是故意的。** 没有它，某个啥都没改的回合会报错（"nothing to commit"），检查点↔回合的对齐就会漂移。cline 正是为此始终允许空提交。
3. **恢复是*硬*重置，而这正是要点。** 它覆盖用户磁盘上的真实文件。目标检查点之后创建的任何东西（一个新文件、一次后续编辑）都从工作树里被移除——这就是"撤销到第 N 步"的含义。
4. **用 `rev-parse HEAD` 读 hash，而不是 commit 子命令的输出。** git 的 `commit` 标准输出格式跨版本不一致；`git rev-parse HEAD` 是读取你刚写入的 SHA 的可移植做法。

## What Changed (vs. s09)

s09 在运行时扩展了工具*注册表*——它启动一个外部 MCP server，并把它的远程工具适配进 executor。s10 在下一层：它完全不新增工具、也不改变循环的控制流。相反，它把一个**存储层**包在循环所做的事之外，于是任何回合的文件改动都可恢复。

```diff
 // s09：注册表从外部进程获得工具。
-tools := registry.All()                 // 编译期工具
+remote, _ := hub.ListTools(ctx)          // 通过 JSON-RPC 的 tools/list
+for _, rt := range remote {
+    registry.Register(adaptMCPTool(hub, rt))
+}

 // s10：每回合后给工作区拍快照；任何回合都可恢复。
+sg := &ShadowGit{}
+sg.Init(workspace, shadowDir)            // 独立的 --git-dir，工作树 = 工作区
+for turn := range loop {
+    // ... 运行本回合已审批的工具（s03/s04），应用编辑（s07）...
+    hash, _ := sg.Commit(fmt.Sprintf("turn %d", turn))  // 检查点
+    timeline = append(timeline, Checkpoint{Hash: hash, TurnIndex: turn})
+}
+// 用户按下撤销：
+sg.Restore(timeline[7].Hash)             // git reset --hard——文件回弹
```

语义上的转变：之前所有章节都在*向前*走（解析 → 分发 → 执行 → 编辑）。s10 是第一个让智能体*向后*走的机制——一份持久的、逐步的回退，且不花用户真实历史一分一毫。

## Try It

测试无需联网——只要 PATH 上有 `git`（缺失则干净地 `t.Skip`）：

```bash
cd agents/s10-checkpoints-shadow-git

# demo：编辑 → 检查点 → 再编辑 → 恢复 → 继续链条
go run .

# 在 t.TempDir 的临时工作区里对真实 git 跑全部六个测试
go test -v ./...
```

期望输出形态（hash 与临时路径每次都不同——关注"形态"）：

```
== shadow git checkpoints demo ==
workspace : /tmp/s10-demo-XXXXXX/workspace
shadow dir: /tmp/s10-demo-XXXXXX/shadow-git   (separate GIT_DIR — never the user's .git)
[init] shadow repo created with an initial commit
[turn 1] wrote app.go              -> checkpoint 91e5e0dd
[turn 2] edited app.go + util.go   -> checkpoint 3804a6af
workspace now:
  - app.go
  - util.go
[restore] resetting workspace to checkpoint 91e5e0dd (end of turn 1)
workspace after restore:
  - app.go
app.go contents: "package main\n\nfunc main() {}\n"
[safety] workspace/.git does NOT exist — the user's history was never touched
```

跑对了的标志：`util.go` 在第 2 回合后存在，恢复到第 1 回合检查点后**消失**，而 `[safety]` 这行确认工作区里从未创建 `.git`。如果没有 git，你会看到 "git is not installed"，测试随即跳过。

## Upstream Source Reading

cline 的影子 git 住在两个文件里：`CheckpointGitOperations.ts` 负责底层 git 管道（init、暂存、commit），`CheckpointTracker.ts` 编排生命周期（create → commit → resetHead）。带注解的完整节选在 [`upstream-readings/s10-checkpoints-shadow-git.ts`](https://github.com/Ding-Ye/learn-cline/blob/main/upstream-readings/s10-checkpoints-shadow-git.ts)。核心是 `initShadowGit`：

```upstream:apps/vscode/src/integrations/checkpoints/CheckpointGitOperations.ts#L59-L115
public async initShadowGit(gitPath: string, cwd: string, taskId: string): Promise<string> {
	// ... 嵌套仓库清理，然后：若影子已存在，只校验 worktree ...

	const git = simpleGit(checkpointsDir)
	await git.init() // git 目录在 cline 的存储里，不在工作区

	// 关键一行：把这个仓库的工作树指向用户的工作区。
	// 现在 `git add`/`commit` 给工作区拍快照，但每个对象/ref
	// 都写在 checkpointsDir 之下——对用户真实的仓库不可见。
	await git.addConfig("core.worktree", cwd)
	await git.addConfig("commit.gpgSign", "false")
	await git.addConfig("user.name", "Cline Checkpoint")
	await git.addConfig("user.email", "checkpoint@cline.bot")

	const lfsPatterns = await getLfsPatterns(cwd)
	await writeExcludesFile(gitPath, lfsPatterns) // 排除巨大/LFS 文件

	const addFilesResult = await this.addCheckpointFiles(git) // 跑 `git add .`
	if (!addFilesResult.success) {
		throw new Error("Failed to add at least one file(s) to checkpoints shadow git")
	}
	// 初始提交，于是永远有一个 HEAD 可供 reset 回去
	await git.commit("initial commit", { "--allow-empty": null, "--no-verify": null })
	return gitPath
}
```

**对照阅读要点**：

- **`core.worktree` vs. 我们的显式 flag**：上游把工作树*一次性*写进影子配置（`addConfig("core.worktree", cwd)`），于是那个仓库里普通的 `git` 命令会自动解析工作区。我们则在每次 `os/exec` 调用上传 `--work-tree`——同一机制，只是在每个调用点显式可见而非配置一次。
- **`simple-git` vs. `os/exec`**：上游通过 `simple-git` 库驱动 git；我们直接 shell 出 `git` 二进制。实际子命令（`init`、`add .`、`commit --allow-empty --no-verify`、`reset --hard`）一模一样——那个库只是同一套管道上的薄封装。
- **嵌套 git 仓库**：上游在暂存前会把任何嵌套 `.git` 临时改名成 `.git_disabled`（`renameNestedGitRepos`），因为 git 不用 submodule 就不会追踪"仓库套仓库"。我们的 mini 在扁平的临时工作区上操作，完全省略了这一点——对教学场景正确，对真实 monorepo 不完整。
- **"原始工作区"守卫**：重新初始化一个已存在的影子时，上游检查 `core.worktree === cwd` 并抛出 "Checkpoints can only be used in the original workspace"。我们在 demo/测试里总是新建影子，所以不需要这个守卫——但它正是防止影子被指向错误文件的安全栏。
- **我们故意省掉的锁与排除**：真实的 `commit()`/`resetHead()` 会获取文件夹锁（`tryAcquireCheckpointLockWithRetry`），让两个 cline 实例不会同时拍快照；init 还会写一个 LFS 感知的排除文件，让巨大的二进制不撑爆快照。两者都正确但与核心机制正交，所以 s10 把它们留在外面。

**想读更多**：从 `CheckpointGitOperations.ts` 的 `initShadowGit`（L59）入手，看影子被创建、`core.worktree` 被设置，然后跟着生命周期进 `CheckpointTracker.ts` 的 `create`（L127）→ `commit`（L212）→ `resetHead`（L336）。安全栏部分，分叉进 `addCheckpointFiles` → `renameNestedGitRepos`（L148）、`CheckpointExclusions.ts`、`CheckpointLockUtils.ts`。这条线就是 s10 背后真实的代码地图。

---

**下一节预告**：这是最后一章。`s_full-integration` 把 s01–s10 串成一个连贯的智能体——循环流式读取、解析、分发、把关、编辑、**每回合后拍检查点（本章）**、并裁剪上下文，其中一个工具来自 MCP。整体才是 cline 智能体，而不是一堆零件。
