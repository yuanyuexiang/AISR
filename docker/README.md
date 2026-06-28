# 在 Docker 容器里调用 AISR(拓扑 A)

**拓扑 A:调用方在容器,AISR daemon 在宿主机。** AISR 复用宿主机已登录的
`claude` / `cursor-agent`(零 API key),容器里的程序通过 **TCP**(`host.docker.internal`)
调用它。

> 为什么走 TCP 而不是挂载 Unix socket?在 **macOS / Docker Desktop** 上,宿主进程在
> macOS、容器在 Linux VM 里,把宿主的 Unix socket 挂进容器**连不通**。TCP +
> `host.docker.internal` 是可靠路径。(纯 Linux 宿主可以挂 socket,见末尾。)
>
> **Windows(Docker Desktop)** 同理:用 TCP,PowerShell 里把下面的
> `AISR_TOKEN=123 ...` 换成先 `$env:AISR_TOKEN="123"` 再执行命令。详见
> [../docs/windows.md](../docs/windows.md)。

## 安全前提:TCP 必须带 token

TCP 监听是网络可达的,所以 daemon **强制要求 bearer token**(不给就拒绝启动)。
**自己定一个值,两个终端用同一个**(下面用 `123` 做示例,生产请换成强随机值)。
`0.0.0.0` 会暴露到局域网——token 是这层的唯一防线;不需要外部访问时也可只绑到
Docker 网关而非 `0.0.0.0`(见末尾)。

## 步骤

### 1) 宿主机:带 token 启动 daemon(TCP)

```bash
cd /path/to/AISR
go build -o ./bin/aisr ./cmd/aisr

AISR_TOKEN=123 ./bin/aisr serve --listen 0.0.0.0:7878    # 看到 listening on ... 即可
```

### 2) 容器:跑 Python 调用方(同一个 token)

```bash
# 在另一个终端,仓库根目录(token 跟上面一致):
AISR_TOKEN=123 docker compose -f docker/docker-compose.yml \
  run --rm caller "用一句话回答:1+1等于几"
```

容器里的 [example.py](../clients/python/example.py) 通过环境变量
`AISR_BASE_URL=http://host.docker.internal:7878` 和 `AISR_TOKEN` 自动连上宿主机
daemon。换 provider:在 prompt 前不用改,客户端默认 claude;要用 cursor 就调
`c.send(name, prompt, provider="cursor")`(改 example.py 或自己写)。

### 用 curl 从容器里测(不依赖 Python)

```bash
docker run --rm --add-host=host.docker.internal:host-gateway curlimages/curl \
  -s -H "Authorization: Bearer 123" \
  http://host.docker.internal:7878/v1/providers
```

### 自己的容器(Python)里接入

```python
from aisr import Client          # 把 clients/python 放进镜像或 PYTHONPATH
c = Client()                     # 读 AISR_BASE_URL / AISR_TOKEN 环境变量
for ev in c.send("dev", "优化这个项目", provider="cursor"):
    if ev.kind == "text":
        print(ev.text, end="", flush=True)
```

Go SDK 同理:`sdk.New()` 也读 `AISR_BASE_URL` / `AISR_TOKEN`,或显式
`sdk.New(sdk.WithBaseURL("http://host.docker.internal:7878"), sdk.WithToken(tok))`。

## 排错

- **连不上 / connection refused**:确认宿主 daemon 绑的是 `0.0.0.0`(不是 `127.0.0.1`,
  否则容器到不了);Linux 上确认容器有 `--add-host=host.docker.internal:host-gateway`
  (compose 里已加)。
- **401 UNAUTHORIZED**:容器和宿主的 `AISR_TOKEN` 不一致。
- **provider 报未登录**:在宿主机先 `claude -p hi` / `cursor-agent status` 确认登录正常
  ——认证是宿主机的事,与容器无关。

## 更稳的绑定(可选)

不想暴露到整个局域网时,绑到 Docker 网关 IP 而非 `0.0.0.0`(Docker Desktop 一般是
`192.168.65.x` 或用 `host.docker.internal` 对应的网关),或加防火墙规则。token 仍是必须的。

## 纯 Linux 宿主:可改用挂载 Unix socket

如果**宿主也是 Linux**(与容器共享内核),可以不走 TCP,直接把 socket 挂进容器、零 token:

```bash
./bin/aisr serve                                  # 默认 ~/.aisr/aisr.sock
docker run --rm -v ~/.aisr/aisr.sock:/aisr.sock \
  -e AISR_SOCKET=/aisr.sock -v "$PWD/clients/python:/app" -w /app \
  python:3.12-slim python3 example.py "你好"
```
(macOS / Docker Desktop 不适用——见开头说明。)
