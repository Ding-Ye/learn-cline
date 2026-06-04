# s10 · Checkpoints via Shadow Git / 影子 Git 检查点

> A second, hidden git repo snapshots the workspace after every step — undo without touching the user's history.
> 一个隐藏的第二 git 仓库在每一步后给工作区拍快照——撤销而不碰用户的历史。

Part of **learn-cline** — a 10-chapter Go curriculum that rebuilds cline's
coding-agent core from scratch. Upstream: [cline/cline](https://github.com/cline/cline)
@ `a209825116dca469c80af4be53989638dd329f38`. This chapter is a self-contained
Go module (`learn-cline/s10`, go 1.23, stdlib only). It is the capstone
mechanism — durable per-step undo around everything the loop does.

本章是 **learn-cline** 课程的第 10 章（最后一章），也是最具特色的收官机制：
为智能体的每一步提供持久的撤销能力。每一章都是独立的 Go module，只用标准库。

---

## What this teaches / 本章要点

An agent that edits files needs **undo**, but it must NOT commit to the user's
real git history after every step. cline's answer is a **shadow git**: a second
git repository whose object store (`--git-dir`) lives *outside* the workspace,
while its work-tree (`--work-tree`) IS the workspace. It snapshots after each
turn (`git add -A` + `git commit`, returning a hash) and undoes with
`git reset --hard <hash>` — all invisible to the project's real `.git`.

编辑文件的智能体需要**撤销**，但绝不能在每一步后往用户真实的 git 历史里提交。
cline 的做法是**影子 git**：一个第二 git 仓库，它的对象库（`--git-dir`）放在
工作区*之外*，而它的工作树（`--work-tree`）就是用户的工作区。每一回合后拍快照
（`git add -A` + `git commit`，返回 hash），撤销时用 `git reset --hard <hash>`
——对项目真实的 `.git` 完全不可见。

```
       ┌──────────────────── one git invocation ─────────────────────┐
       │ git --git-dir=<shadowDir> --work-tree=<workspace> <subcmd>   │
       └──────────────────────────────────────────────────────────────┘
            │ object store = shadowDir        │ tracked files = workspace
            ▼                                  ▼
   ┌───────────────┐   Init     ┌──────────────────────────────┐
   │  shadow .git  │◀───────────│  workspace (the user's files) │
   │ (hidden, NOT  │   Commit   │   app.go  util.go  ...        │
   │  workspace/   │──hash────▶ │                               │
   │  .git)        │  Restore   │  reset --hard rewinds files   │
   └───────────────┘─────files─▶└──────────────────────────────┘
        List → [hash, hash, ...]   (the undo timeline)
```

---

## Files / 文件

| File | Role |
|------|------|
| `provider.go` | canonical `Checkpoint` type (one shadow-git snapshot record) |
| `checkpoint.go` | `ShadowGit`: `Init` / `Commit(message)->hash` / `Restore(hash)` / `List`; the `git` helper that prepends `--git-dir`/`--work-tree` to every call |
| `main.go` | demo: edit files → snapshot → edit more → restore → continue the chain |
| `checkpoint_test.go` | 6 tests against a real git binary in `t.TempDir`; `t.Skip` when git is absent |

---

## Try it / 动手试一试

Tests need no network — only a `git` binary (they `t.Skip` if it's missing) /
测试无需联网，只需 `git`（缺失则自动跳过）：

```bash
cd agents/s10-checkpoints-shadow-git
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...   # see each case / 看每个用例
```

Watch the full lifecycle with the demo / 用 demo 观察完整生命周期
(edit → checkpoint → edit → restore, all in a throwaway temp dir):

```bash
go run .
```

Expected output shape is in [`testdata/expected.txt`](testdata/expected.txt) —
the commit hashes and temp paths differ every run, so watch the *shape*:
util.go appears after turn 2, then vanishes after restoring the turn-1
checkpoint, and `workspace/.git` never exists.

预期输出形态见该文件——hash 与临时路径每次都不同，关注"形态"：util.go 在第 2 回合
出现，恢复到第 1 回合检查点后消失，且 `workspace/.git` 始终不存在。

---

## Deliberately omitted / 故意省略

cline's real `CheckpointTracker` also acquires a folder lock (multi-instance
safety), temporarily disables nested `.git` repos (git won't track them without
submodules), writes an LFS-aware excludes file, emits subscription events, and
computes diffs between checkpoints. s10 teaches only the **mechanism**: a
separate `--git-dir` over the workspace work-tree, init → commit → reset. Locks,
nested-repo handling, exclusions, and diffing are left out on purpose.

cline 真实的 `CheckpointTracker` 还会获取文件夹锁（多实例安全）、临时禁用嵌套
`.git` 仓库、写 LFS 感知的 excludes 文件、发送订阅事件、计算检查点间的 diff。
s10 只教**机制**：在工作区工作树上用独立的 `--git-dir`，init → commit → reset。
锁、嵌套仓库处理、排除规则与 diff 都故意省略。

See `docs/en/s10-checkpoints-shadow-git.md` (and `docs/zh/...`) for the full
six-section walkthrough and the annotated upstream excerpt.

完整六段式讲解与上游源码注解见 `docs/{en,zh}/s10-checkpoints-shadow-git.md`。
