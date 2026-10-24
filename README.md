# TrollVNC 屏幕墙

单路 H.264 → MediaMTX（仅 publish）→ Go 零转码 HTTP-TS → mpegts.js 网格墙。

## 依赖

- Go 1.22+
- MediaMTX（建议 ≥1.9，内部用户认证建议 ≥1.21.1）
- ffmpeg（PATH 内，TS 网关 `-c copy` remux）
- 现代 Chromium / Edge（MSE）

## 快速启动

```bash
# 1. 改 config.yaml / mediamtx.yml 中的 internal 密码（两处一致）
# 2. 启动 MediaMTX
mediamtx mediamtx.yml

# 3. 启动 Go
cd backend
go mod tidy
go run . -config ../config.yaml

# 4. 浏览器
# http://127.0.0.1:18082/
```

登记设备：页面「登记设备」写入 `deviceId+token`。  
iPhone 推流：`rtsp://<host>:8554/<deviceId>?token=<token>`（TCP）。  
控制 TCP：`<host>:19000`，首帧 `hello`。

## 端口（默认，均可配）

| 服务 | 地址 |
|------|------|
| 控制 TCP | `:19000` |
| MTX auth HTTP | `127.0.0.1:18080` |
| HTTP-TS | `:18081` → `/{deviceId}.ts` |
| API + 前端 | `127.0.0.1:18082` |
| MediaMTX RTSP/API | `:8554` / `:9997` |

## 文档

见 [docs/protocol.md](docs/protocol.md)。

## 联调假设备（仅控制面）

```bash
# 另开终端；先保证 wall 已启动
cd backend
go run ./tools/fake_phone -id phone-demo -token demo-token
```

页面应出现设备（ctrl 在线；无推流则仍 offline）。真机推流后再变 preview。

## 说明

- 墙端唯一读出口是 Go TS；MTX 关闭 HLS/WebRTC/RTMP/SRT。
- 启动会打 **startup snapshot**（内部 read 路径等），运行期不重推断。
- TS 网关当前用 **ffmpeg copy→mpegts** 管生命周期；`internal/tsgateway/mpegts.go` 保留纯 Go mux 骨架供后续替换。
- 控制面 `hello` **必须先登记 token**，禁止设备自注册。
