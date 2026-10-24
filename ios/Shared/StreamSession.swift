import Foundation
import CoreVideo

/// 彩条 → H264 → RTSP + 控制通道。
final class StreamSession {
    private let encoder = H264Encoder()
    private let rtsp = RTSPPublisher()
    private let ctrl = CtrlClient()
    private var timer: DispatchSourceTimer?
    private var frameIndex: UInt64 = 0
    private var width = 1280
    private var height = 720
    private var fps = 15
    private var bitrateKbps = 600
    private var running = false
    private var deviceId = "phone"
    var onLog: ((String) -> Void)? {
        didSet {
            rtsp.onLog = onLog
            ctrl.onLog = onLog
        }
    }

    func start(host: String, deviceId: String, token: String, name: String) {
        stop()
        self.deviceId = deviceId
        running = true
        ctrl.onSetQuality = { [weak self] mode, seq, forceIdr, w, h, br, fps in
            self?.applyQuality(mode: mode, forceIdr: forceIdr, w: w, h: h, br: br, fps: fps)
            self?.onLog?("set_quality mode=\(mode) seq=\(seq) \(w)x\(h) @\(br)kbps/\(fps)fps")
        }

        do {
            try encoder.start(
                width: Int32(width), height: Int32(height),
                fps: Int32(fps), bitrateBps: Int32(bitrateKbps * 1000)
            )
            encoder.onFrame = { [weak self] avcc, key, ptsUs in
                guard let self else { return }
                self.rtsp.sendAVCCFrame(
                    avcc, isKeyframe: key, ptsUs: ptsUs,
                    sps: self.encoder.sps, pps: self.encoder.pps
                )
            }
            // 先 RTSP（已验证通），再 ctrl，避免 ctrl 阻塞/失败拖垮推流
            try rtsp.connect(
                host: host, port: 8554, path: deviceId, token: token,
                width: width, height: height, fps: fps
            )
            startTimer()
            onLog?("streaming \(width)x\(height) @\(fps)fps \(bitrateKbps)kbps")
            ctrl.connect(host: host, port: 19000, deviceId: deviceId, token: token, name: name)
        } catch {
            onLog?("start failed: \(error.localizedDescription)")
            stop()
        }
    }

    func stop() {
        running = false
        timer?.cancel()
        timer = nil
        encoder.stop()
        rtsp.disconnect()
        ctrl.disconnect()
    }

    private func applyQuality(mode: String, forceIdr: Bool, w: Int, h: Int, br: Int, fps: Int) {
        width = max(160, w)
        height = max(160, h)
        bitrateKbps = max(100, br)
        self.fps = max(5, fps)
        do {
            try encoder.start(
                width: Int32(width), height: Int32(height),
                fps: Int32(self.fps), bitrateBps: Int32(bitrateKbps * 1000)
            )
            encoder.onFrame = { [weak self] avcc, key, ptsUs in
                guard let self else { return }
                self.rtsp.sendAVCCFrame(
                    avcc, isKeyframe: key, ptsUs: ptsUs,
                    sps: self.encoder.sps, pps: self.encoder.pps
                )
            }
            startTimer()
            if forceIdr, let pb = TestPattern.make(width: width, height: height, index: frameIndex, label: deviceId) {
                encoder.encode(pixelBuffer: pb, ptsUs: frameIndex &* 66_666, forceKeyframe: true)
            }
            _ = mode
        } catch {
            onLog?("reconfig failed: \(error.localizedDescription)")
        }
    }

    private func startTimer() {
        timer?.cancel()
        let t = DispatchSource.makeTimerSource(queue: DispatchQueue.global(qos: .userInitiated))
        let interval = 1.0 / Double(max(fps, 1))
        t.schedule(deadline: .now(), repeating: interval)
        t.setEventHandler { [weak self] in
            guard let self, self.running else { return }
            self.frameIndex &+= 1
            guard let pb = TestPattern.make(
                width: self.width, height: self.height,
                index: self.frameIndex, label: self.deviceId
            ) else { return }
            let pts = self.frameIndex &* UInt64(1_000_000 / max(self.fps, 1))
            self.encoder.encode(pixelBuffer: pb, ptsUs: pts, forceKeyframe: self.frameIndex % UInt64(max(self.fps * 2, 1)) == 0)
        }
        t.resume()
        timer = t
    }
}
