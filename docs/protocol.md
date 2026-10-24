# TrollVNC 屏幕墙协议

## 架构

```
iPhone --RTSP TCP publish+token--> MediaMTX
Go TS  --internalUser read------> MediaMTX
前端   --HTTP MPEG-TS----------> Go :ts_listen /{deviceId}.ts
iPhone --TCP 控制 JSON----------> Go :ctrl_listen
前端   --HTTP API/SSE-----------> Go :api_listen
```

Codec：**H.264**。墙端容器：**HTTP-TS + mpegts.js**。

## 控制通道

帧：`[u32 BE length][utf8 json]`，`MaxFrameSize=65536`，超额断连。

### 设备 → Go

| type | 说明 |
|------|------|
| `hello` | `{deviceId,token,name}` 握手（**须已 API 登记**，禁止自注册） |
| `heartbeat` | 500ms；连续 5 次扫描未刷新 → ctrl 离线 |
| `ack` | 可靠指令确认，带 `seq` |
| `switch_ack` | 编码侧已改参；**不**单独结束 switching |
| `move` | 覆盖型触摸，不重传 |

### Go → 设备

| type | 说明 |
|------|------|
| `hello_ok` | 握手成功 |
| `set_quality` | `{mode,forceIdr,width,height,bitrateKbps,fps}` + `seq` |
| `error` | `{error: code}` |

可靠指令：超时重传 ≤2；`IsSwitching` 时清重传队列。新 TCP：**先 dead 旧 session**，旧 seq → `switch_seq_stale`。

## 三态与 UI 四态

- `ctrlOnline` / `mtxPublisherUp` / `tsPullHealthy`
- `streamOnline = mtxPublisherUp ∧ tsPullHealthy`
- UI：`offline` | `preview` | `switching` | `hd`（可用需 ctrl∧stream）

撕裂：

- `ctrlOnline=false` → offline，回收 HD，cancel TS
- `ctrl∧!stream` → UI offline，保留 TCP，不保留 HD

## 切换状态机

1. `hd/acquire` → `BeginSwitch` + `set_quality(forceIdr)`  
2. 等 `switch_ack`（编码）+ **`idr_seen`(前端) 或 `idr_emitted`(TS 首包)**，且 `switchSeq` 为当前最新  
3. 3s 超时 → 回滚 preview  
4. 前端 destroy 须 `switch/cancel`  

## HD 租约

- 全局单焦点；踢最早非焦点；switching 中不可踢  
- 排队 1 条，绑 `switchSeq`+session；退出 switching **单次**重评估；超时 3s  
- 失败码见下；前端 **禁止自动重试**

## HTTP API（前端）

| 路径 | 作用 |
|------|------|
| `POST /api/devices/register` | 预下发 deviceId+token |
| `GET /api/devices` | 列表 |
| `POST /api/hd/acquire` | 申请高清 |
| `POST /api/hd/release` | 回预览 |
| `POST /api/focus` | 单焦点 |
| `POST /api/switch/idr_seen` | `{deviceId,switchSeq,...}` |
| `POST /api/switch/cancel` | 取消切换 |
| `POST /api/session/subscribe|unsubscribe|visibility` | Wails/页生命周期 |
| `GET /api/events?sessionId=` | SSE 状态 |
| `GET /{deviceId}.ts` | HTTP-TS（ts_listen） |

## 错误码

`max_hd` `target_switching` `queue_timeout` `not_ctrl_online` `ts_pull_failed` `idr_timeout` `mtx_publisher_lost` `auth_publish_only` `auth_publish_denied` `auth_rate_limited` `mtx_api_error` `session_dead` `switch_seq_stale` `device_destroyed` `page_not_visible`

## 时序（必测）

1. **HD 排队**：acquire 时目标 `switching` → 返回 `target_switching` + 排队 1 条（绑 switchSeq/session）→ 目标释锁后 **单次** `ReevaluateOnce` → LRU 踢非焦点 → `set_quality`；排队 3s → `queue_timeout`；前端禁止自动重试  
2. **TCP 先断**：Read 超时/断连 → 旧 session `dead` → 回收 HD + cancel TS；即使 `mtxPublisherUp` 仍真，UI offline且禁新 HD  
3. **MTX publisher 消失、TCP 仍在**：stream_sync 置 `mtxPublisherUp=false` → `streamOnline=false` → UI offline；保留 TCP；不保留 HD  
4. **TS 拉失败、publisher 仍在**：gateway 置 `tsPullHealthy=false` → 半健康；非整机误杀 ctrl；错误 `ts_pull_failed` / `idr_timeout`  
5. **前端 destroy 仍拉流**：destroyPlayer cancel switchSeq → pause/unload/detach → abort/remove（超时）→ destroy → 关 HTTP → gateway cancel 旧 writer → TEARDOWN/ffmpeg 退出 → 新连接才 remux  

### 切换锁 vs 重传

`IsSwitching=true` 时丢弃新 `set_quality`，并 `ClearRetransmit`；锁仅由最新 seq 的 `idr_seen`/`idr_emitted` 或 3s 超时释放。

## MTX 鉴权

- HTTP auth：**仅 `action==publish`**  
- Go 拉流：**authInternalUsers**（默认）；Exclude **非 ACL**，须实发能力验证才用  
- 启动快照固化，运行期不重推断  
