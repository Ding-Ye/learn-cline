package main

// provider.go — canonical types for s10.
//
// Every chapter in learn-cline is a SELF-CONTAINED Go module: it copies the
// subset of the shared types catalog (see .learn/plan.md) that it actually
// needs, rather than importing a shared package. s10 only needs ONE shared
// type — `Checkpoint` — because this chapter is about the storage layer
// underneath the loop, not the loop itself.
//
// In the real cline the loop (s01), the parser (s02), the tool registry (s03),
// and so on all feed into this: after each turn the Task asks the
// CheckpointTracker to snapshot the workspace, and records the returned hash as
// a Checkpoint. Restoring a checkpoint rewinds every file the agent touched —
// without ever touching the user's real git history.

// Checkpoint is one shadow-git snapshot.
//
// Mirrors the per-turn record cline keeps in
// apps/vscode/src/integrations/checkpoints/CheckpointTracker.ts: each call to
// CheckpointTracker.commit() returns a commit hash, which the Task associates
// with the turn that produced it. Hash is the shadow-git commit SHA; TurnIndex
// is which agent turn created it; Message is the commit message; CreatedAt is a
// Unix timestamp.
type Checkpoint struct {
	Hash      string `json:"hash"`
	TurnIndex int    `json:"turn_index"`
	Message   string `json:"message"`
	CreatedAt int64  `json:"created_at"`
}
