---
title: "s09 · MCP 外部工具集成"
chapter: 9
slug: s09-mcp-integration
est_read_min: 11
---

# s09 · MCP 外部工具集成

> 本节讲什么：一个基于 JSON-RPC 2.0 的最小 MCP（模型上下文协议）客户端——`initialize`、`tools/list`、`tools/call`——它发现外部服务器的工具，并把它们作为一等公民 `Tool` 合并进注册表。它单独成章，是因为"在运行时、从一个你没有编译的进程扩展智能体的能力"，与 s03 那种编译进来的注册表，是结构上完全不同的一步。

---

## Problem

s03 给了智能体一个工具注册表，并按名字分发工具，但其中每一个工具都是 *编译进二进制* 的。这个天花板是真实的：你不可能为用户可能拥有的每一个数据库、SaaS API、内部服务都内置一个工具，更不可能每次有人想要一个新能力就重新编译 cline。

MCP（模型上下文协议）就是那个逃生口：cline 通过一个 JSON-RPC 传输与 **外部服务器** 对话，询问它提供哪些工具，并把它们像内置工具一样暴露给模型。本章要解决的痛点是接线——你如何连接到这样一个服务器，用注册表已经理解的词汇发现它的工具，并把一次工具调用路由给它——而且当服务器（一个你无法控制的东西）行为异常时，不能让循环崩溃。

## Solution

心智模型：一个 MCP 服务器就是一个回答三个方法的 JSON-RPC 2.0 对端。客户端 (1) 做一次 `initialize` 握手以约定协议版本，(2) 调用 `tools/list` 并把每个返回的定义转换成我们的标准 `ToolSchema`，(3) 把每次调用路由到 `tools/call`。转换后的 schema 直接落进 s03 的注册表，于是远程工具被分发的方式和本地工具完全一样。

三个关键决策点：

1. **把传输放在接口背后。** 客户端面对的是一个 `Transport`，而不是一个子进程。在生产里它是到子进程的 stdio；在测试里它是一个 **假的** 进程内服务器。客户端区分不出来——这正是整章封闭可复现的原因。
2. **发现的工具是一等 `Tool`。** 一个 `mcpTool` 适配器包装每个远程定义：`Schema()` 返回 `tools/list` 给我们的东西，`Execute()` 路由到 `tools/call`。注册表持有 `Tool`，从不知道哪些是远程的。
3. **区分协议失败与工具失败。** 缺失的连接或畸形的消息是 JSON-RPC 错误（一个 Go `error`）；一个运行了但失败的工具会以 `isError:true` 的结果返回。模型能读到后者并恢复——绝不崩溃。

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────┐
│  McpClient                          MCP 服务器 (fake/stdio)  │
│     │   initialize  ───────────────────▶  约定版本           │
│     │   ◀───────────  InitializeResult                      │
│     │   tools/list  ───────────────────▶  枚举工具           │
│     │   ◀───────────  [ {name, schema} ... ]                │
│     │        │ 逐个转换 → ToolSchema                         │
│     │        ▼                                              │
│   Registry.Merge([]Tool)   ← 远程工具现在是一等公民          │
│     │   tools/call {name,args} ────────▶  运行工具           │
│     │   ◀───────────  { content, isError }                  │
└────────────────────────────────────────────────────────────┘
```

`mcp.go` 的核心——`ListTools` 发出请求并把每个远程定义转换成注册表的词汇，外加让发现的工具可被分发的那个适配器：

```go
// ListTools 发出 "tools/list" 并把每个远程定义转换成我们标准的 ToolSchema——本章的关键。
func (c *McpClient) ListTools() ([]ToolSchema, error) {
	if !c.initialized {
		return nil, fmt.Errorf("ListTools: client not initialized (call Initialize first)")
	}
	var res listToolsResult
	if err := c.call("tools/list", struct{}{}, &res); err != nil {
		return nil, err
	}
	out := make([]ToolSchema, 0, len(res.Tools))
	for _, t := range res.Tools {
		out = append(out, ToolSchema{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return out, nil
}

// mcpTool 把一个发现的 MCP 工具适配进注册表的 Tool 接口，于是服务器提供的工具
// 被分发的方式和编译进来的工具完全一样。
type mcpTool struct {
	client *McpClient
	schema ToolSchema
}

func (t *mcpTool) Schema() ToolSchema { return t.schema }

func (t *mcpTool) Execute(args map[string]interface{}) (string, error) {
	res, err := t.client.CallTool(t.schema.Name, args)
	if err != nil {
		return "", err // 协议/传输失败
	}
	if res.IsError {
		return "", fmt.Errorf("%s", res.Text()) // 工具级失败 → 错误结果
	}
	return res.Text(), nil
}
```

**四个非显然之处**：

1. **`initialize` 必须先来。** `ListTools`/`CallTool` 拒绝在未初始化的客户端上运行。真实服务器跟踪生命周期状态，可能在握手前拒绝工具流量——cline 在任何 `fetchToolsList` 之前、在 `connectToServer` 里运行它。
2. **`inputSchema` 几乎是字段拷贝。** 一个远程工具已经用 JSON-Schema 对象描述自己，和 `ToolSchema.InputSchema` 是同一个想法。发现是转换而非翻译——这正是 MCP 工具能如此干净地合并的 *原因*。
3. **两条错误通道。** `c.call` 对协议问题返回 `*RPCError`（未知方法 `-32601`、解析错误 `-32700`）；失败的工具返回带 `IsError:true` 的结果。适配器把后者收束成一个 Go error，于是执行器把它包成一个错误 `tool_result`（s03 的不崩溃纪律）。
4. **传输接缝让测试封闭。** 因为客户端只看到 `Transport.RoundTrip([]byte) []byte`，假服务器 *就是* 那个传输——没有子进程、没有管道。同一个接缝就是真实 stdio 客户端把数据 marshal 到子进程 stdin 的地方。

## What Changed (vs. s03)

```diff
  type Registry struct {
  	tools map[string]Tool
  }

+ // Merge 把一批工具（比如一个 MCP 服务器暴露的全部）折叠进注册表——这是远程服务器的
+ // 能力成为智能体一部分的那一刻。
+ func (r *Registry) Merge(tools []Tool) {
+ 	for _, t := range tools {
+ 		r.Register(t)
+ 	}
+ }

+ // mcpTool：一个 Execute() 是远程 tools/call、而非本地代码的 Tool。
+ type mcpTool struct {
+ 	client *McpClient
+ 	schema ToolSchema
+ }
+ func (t *mcpTool) Schema() ToolSchema { return t.schema }
+ func (t *mcpTool) Execute(args map[string]interface{}) (string, error) { /* → tools/call */ }
```

在 s03 里，注册表在启动时被一次性填充，工具的 `Execute` 在进程内运行。s09 的语义变化是：注册表在 **运行时被扩展**，加入的工具其 `Execute` 是对一个你没写过的进程的网络调用。模型看不出区别；注册表看不出区别。只有适配器知道另一端有一个 JSON-RPC 服务器。

## Try It

```bash
# 无网络、无子进程、无 API key——完全确定性。
cd agents/s09-mcp-integration

# 连接进程内假服务器，发现 + 合并 + 调用
go run .

# 同时把 initialize 握手打印到 stderr
go run . -v

# 测试
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -v ./...
```

期望输出形态：

```
discovered 2 MCP tool(s):
  - add    Add two integers a and b.
  - echo   Echo back the provided text.

calling tools through the registry:
  echo   -> ok    hello from MCP
  add    -> ok    42
  echo   -> ERROR echo requires a non-empty 'text' argument
```

这两个工具是 *从服务器发现的*，不是编译进来的；最后那个 ERROR 是一个以结果形式浮现的工具级失败（不是崩溃）。加上 `-v`，`[s09] handshake ok: server=fake-mcp-server ...` 会先打印到 stderr。

## Upstream Source Reading

cline 的 MCP 集成在 `McpHub.ts` 里。`connectToServer`（L286）打开一个传输并连接 SDK 的 `Client`（它的 `connect()` 运行 `initialize` 握手），然后拉取服务器的工具；`fetchToolsList`（L677）发出 `tools/list`；`callTool`（L1233）发出 `tools/call`。最大的差别：cline 把 JSON-RPC 线协议以及 stdio/SSE 传输委托给 `@modelcontextprotocol/sdk`，而 s09 手写协议并让它经过一个 `Transport` 接口，于是一个假服务器可以顶上。

```upstream:apps/vscode/src/services/mcp/McpHub.ts#L677-L711
private async fetchToolsList(serverName: string): Promise<McpTool[]> {
	try {
		const connection = this.connections.find((conn) => conn.server.name === serverName)
		if (!connection) {
			throw new Error(`No connection found for server: ${serverName}`)
		}
		// Disabled servers don't have clients, so return empty tools list
		if (connection.server.disabled || !connection.client) {
			return []
		}

		// 核心请求：一个带类型的 JSON-RPC 调用。method "tools/list"，用
		// ListToolsResultSchema 校验，带超时。s09 的 ListTools 就是这个。
		const response = await connection.client.request({ method: "tools/list" }, ListToolsResultSchema, {
			timeout: DEFAULT_REQUEST_TIMEOUT_MS,
		})

		// 读取 autoApprove 设置——通向审批门控（s04）的接缝。
		const settingsPath = await getMcpSettingsFilePathHelper(await this.getSettingsDirectoryPath())
		const content = await fs.readFile(settingsPath, "utf-8")
		const config = JSON.parse(content)
		const autoApproveConfig = config.mcpServers[serverName]?.autoApprove || []

		// 根据设置把工具标记为始终允许（s09 省略这个 flag）。
		const tools = (response?.tools || []).map((tool) => ({
			...tool,
			autoApprove: autoApproveConfig.includes(tool.name),
		}))

		return tools
	} catch (error) {
		// 一个不稳定的服务器返回 [] 而不是抛出——它不能拖垮整个任务。
		//（s09 反而把错误暴露出来，为了教学清晰。）
		Logger.error(`Failed to fetch tools for ${serverName}:`, error)
		return []
	}
}
```

**对照阅读要点**：

- **发现 = `request("tools/list")` + 一个 map 步骤**：上游把每个工具映射成增加一个 `autoApprove` flag；s09 把每个映射成一个 `ToolSchema`。两者都是"把服务器的工具列表转换成我们的词汇"。
- **我们没有的 `autoApprove`**：上游在这里给每个工具打上一个审批 flag，把发现接进 s04 的门控。s09 还没有审批，所以这个字段被丢掉了。
- **异步 SDK vs 手写的同步线协议**：上游的 `connection.client.request(...)` 是 SDK 那个带类型、带 schema 校验、带超时的 JSON-RPC 调用；s09 的 `c.call(method, params, &result)` 是同一个想法，在一个 `Transport` 上手写出来。
- **失败处理刻意不同**：上游吞掉错误并返回 `[]`，这样一个坏服务器不能杀死任务；s09 把错误返回，让学习者准确看到哪里坏了。
- **resources 与 prompts 被省略**：`connectToServer` 还会调用 `fetchResourcesList`/`fetchPromptsList`（L658-660）；s09 只保留 `tools/*`——故意做成正确但不完整。

**想读更多**：从 `McpHub.ts` 的 `connectToServer`（L286）入手，跟着它到 `fetchToolsList` 调用（L657）并进入 `fetchToolsList`（L677），最后读 `callTool`（L1233）。从那里，发现的工具经由 `UseMcpToolHandler` 浮现，它又路由回 `McpHub.callTool`——一个 `Execute()` 是远程调用的注册表条目。这条线就是 s09 → s04（auto-approve）→ s06（工具进 prompt）的真实代码地图。

---

**下一节预告**：s10 加入影子 Git 检查点，于是智能体做的每一次编辑——包括由 MCP 工具驱动的——都能被快照和撤销。
