# WallPhone — 屏幕墙真机推流（tipa）

对接 `trollvnc-wall`：**彩条 H.264 → RTSP TCP** + **控制 TCP**。

## 使用

1. 电脑墙页登记相同 `deviceId` / `token`
2. App 填电脑局域网 IP，点「开始彩条推流」
3. 墙页应出现 preview 画面

## GitHub 打 tipa

Actions → **Build WallPhone tipa** → Run workflow

或本目录本地（需 Mac）：

```bash
brew install xcodegen ldid
cd ios && xcodegen generate
xcodebuild -project WallPhone.xcodeproj -scheme WallPhone \
  -configuration Release -sdk iphoneos -derivedDataPath build \
  -destination 'generic/platform=iOS' \
  CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO build
VERSION=0.1.0 DERIVED_DATA="$PWD/build" ./scripts/pack-tipa.sh ../dist
```
