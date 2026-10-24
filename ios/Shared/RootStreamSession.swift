import Foundation
import RootEncoder
import ReplayKit
import UIKit

/// RootEncoder 屏幕（ReplayKit）→ RTSP TCP（优先 H.265）→ MediaMTX
/// 不依赖相机硬件。
final class RootStreamSession: NSObject, ConnectChecker {
    private var stream: RtspDisplay?
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
        guard RPScreenRecorder.shared().isAvailable else {
            onLog?("ReplayKit 不可用，无法采屏")
            return
        }
        onLog?("采屏模式（无相机） ReplayKit available")
        DispatchQueue.main.async {
            self.begin(host: host, deviceId: deviceId, token: token)
        }
    }

    private func begin(host: String, deviceId: String, token: String) {
        // 竖屏尺寸；编码器失败再降
        let trials: [(Int, Int, Int)] = [
            (720, 1280, 1_200_000),
            (540, 960, 800_000),
            (480, 640, 500_000),
        ]
        let codecs: [VideoCodec] = [.H265, .H264]

        for codec in codecs {
            let s = RtspDisplay(connectChecker: self)
            s.setVideoCodec(codec: codec)
            var vOK = false
            var used = (0, 0, 0)
            for t in trials {
                // DisplayBase: prepareVideo(width:height:fps:bitrate:iFrameInterval:rotation:)
                if s.prepareVideo(width: t.0, height: t.1, fps: 30, bitrate: t.2, iFrameInterval: 2, rotation: 0) {
                    vOK = true
                    used = t
                    break
                }
                onLog?("prepareVideo fail \(t.0)x\(t.1) \(codec)")
            }
            guard vOK else { continue }

            let client = s.getStreamClient()
            client.setOnlyVideo(onlyVideo: true)
            client.setRetries(reTries: 5)
            client.setLogs(enabled: true)

            let url = "rtsp://\(host):8554/\(deviceId)?token=\(token)"
            onLog?("start screen \(used.0)x\(used.1) \(codec) \(url)")
            s.startStream(endpoint: url)
            stream = s
            running = true
            onLog?("若弹出录屏权限请点允许")
            return
        }
        onLog?("prepareVideo failed：编码器起不来")
    }

    func stop() {
        running = false
        if let s = stream {
            if s.isStreaming() { s.stopStream() }
        }
        stream = nil
    }
}
