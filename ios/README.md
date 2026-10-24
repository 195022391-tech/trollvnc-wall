# WallPhone v0.2 — RootEncoder → MediaMTX（H.265）

## 用法

1. 电脑墙登记相同 `deviceId` / `token`
2. App 填电脑局域网 IP，允许相机，点「开始推流」
3. 电脑可用：`ffplay -rtsp_transport tcp "rtsp://127.0.0.1:8554/<deviceId>"`（内部读）或墙页

## 构建 tipa

GitHub Actions → **Build WallPhone tipa** → version `0.2.0`
