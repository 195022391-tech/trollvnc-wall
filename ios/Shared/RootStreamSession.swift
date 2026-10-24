import Foundation
import RootEncoder
import ReplayKit
import UIKit

/// 屏幕（ReplayKit / 窗口快照）→ RTSP TCP → MediaMTX。不依赖相机。
final class RootStreamSession: NSObject, ConnectChecker {
    private var stream: RtspStream?
    private var running = false
    var onLog: ((String) -> Void)?

    func onConnectionSuccess() { onLog?("rtsp: connected") }
    func onConnectionFailed(reason: String) { onLog?("rtsp: failed \(reason)") }
    func onNewBitrate(bitrate: UInt64) {}
    func onDisconnect() { onLog?("rtsp: disconnected") }
    func onAuthError() { onLog?("rtsp: auth error") }
    func onAuthSuccess() { onLog?("rtsp: auth ok") }

    func start(host: String, deviceId: String, token: String, name: String) {
        stop()
        _ = name
        onLog?("采屏推流（无相机）")
        DispatchQueue.main.async {
            self.begin(host: host, deviceId: deviceId, token: token)
        }
    }

    private func begin(host: String, deviceId: String, token: String) {
        // 墙侧 mpegts.js 对 H.265 不友好，优先 H.264；竖屏尺寸
        let trials: [(Int, Int, Int)] = [
            (720, 1280, 1_200_000),
            (540, 960, 800_000),
            (480, 640, 500_000),
        ]
        let codecs: [VideoCodec] = [.H264, .H265]

        for codec in codecs {
            let s = RtspStream(connectChecker: self)
            let src = WallScreenSource()
            src.onInfo = { [weak self] line in self?.onLog?(line) }
            s.changeVideoSource(source: src)
            s.setVideoCodec(codec: codec)
            var vOK = false
            var used = (0, 0, 0)
            for t in trials {
                if s.prepareVideo(width: t.0, height: t.1, bitrate: t.2, fps: 30, iFrameInterval: 2, rotation: 0) {
                    vOK = true
                    used = t
                    break
                }
                onLog?("prepareVideo fail \(t.0)x\(t.1) \(codec)")
            }
            guard vOK else {
                s.release()
                continue
            }

            let client = s.getStreamClient()
            client.setOnlyVideo(onlyVideo: true)
            client.setRetries(reTries: 5)
            client.setLogs(enabled: true)

            let url = "rtsp://\(host):8554/\(deviceId)?token=\(token)"
            onLog?("start screen \(used.0)x\(used.1) \(codec) \(url)")
            s.startStream(endpoint: url)
            stream = s
            running = true
            return
        }
        onLog?("prepareVideo failed：编码器起不来")
    }

    func stop() {
        running = false
        if let s = stream {
            if s.isStreaming() { s.stopStream() }
            s.release()
        }
        stream = nil
    }
}
