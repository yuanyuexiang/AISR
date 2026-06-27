# AISR Python 客户端

一个**仅依赖标准库**的薄客户端,通过本地 Unix socket(默认 `~/.aisr/aisr.sock`)
调用 AISR daemon 的 `/v1` HTTP API。它不重复实现核心逻辑,只是把 socket + HTTP +
NDJSON 解析封装掉。

前提:daemon 已运行(`aisr serve`),且本机已安装并登录 `claude`。

## 用法

```python
from aisr import Client

c = Client()                                  # 默认连 ~/.aisr/aisr.sock
# c = Client(socket_path="/custom/aisr.sock")

# 列出 provider
print([p["name"] for p in c.providers()])

# 一轮对话(session 不存在会自动创建;流式 yield Event)
for ev in c.send("dev", "优化这个 Go 项目"):
    if ev.kind == "text":
        print(ev.text, end="", flush=True)
    elif ev.kind == "error":
        raise RuntimeError(ev.text)

# 再问一次同一个 session = 自动 resume,接上上下文
for ev in c.send("dev", "刚才你建议了什么?"):
    ...

# 会话管理
c.list_sessions()
c.get_session("dev")
c.remove_session("dev")
```

`send()` 返回一个生成器,逐个产出 `Event(kind, text, raw)`;`kind` 取值:
`text` / `tool_use` / `tool_result` / `usage` / `error` / `done`。

## 运行示例

```bash
aisr serve &                                  # 启动 daemon
python3 clients/python/example.py "用一句话回答:1+1等于几"
```
