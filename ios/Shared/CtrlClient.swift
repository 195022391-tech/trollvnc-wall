import Foundation

/// 墙控制通道：[u32 BE len][json] — CFStream + 专用队列 RunLoop（与 RTSP 同 TCP 路径）
final class CtrlClient {
    private var input: InputStream?
    private var output: OutputStream?
    private let queue = DispatchQueue(label: "cn.local.wallphone.ctrl")
    private var running = false
    private var hbTimer: Timer?
    var onLog: ((String) -> Void)?
    var onSetQuality: ((_ mode: String, _ seq: UInt64, _ forceIdr: Bool, _ w: Int, _ h: Int, _ br: Int, _ fps: Int) -> Void)?

    func connect(host: String, port: Int, deviceId: String, token: String, name: String) {
        disconnect()
        var readStream: Unmanaged<CFReadStream>?
        var writeStream: Unmanaged<CFWriteStream>?
        CFStreamCreatePairWithSocketToHost(nil, host as CFString, UInt32(port), &readStream, &writeStream)
        guard let rs = readStream?.takeRetainedValue(), let ws = writeStream?.takeRetainedValue() else {
            onLog?("ctrl: create stream failed")
            return
        }
        let input = rs as InputStream
        let output = ws as OutputStream
        self.input = input
        self.output = output

        let boot = DispatchSemaphore(value: 0)
        var bootErr: String?
        queue.async { [weak self] in
            guard let self else { boot.signal(); return }
            input.schedule(in: .current, forMode: .common)
            output.schedule(in: .current, forMode: .common)
            input.open()
            output.open()
            do {
                try Self.waitOpen(input, output, timeoutMs: 8000)
            } catch {
                bootErr = error.localizedDescription
                boot.signal()
                return
            }
            self.running = true
            boot.signal()
            self.onLog?("ctrl: tcp ready")

            let hello: [String: Any] = [
                "type": "hello",
                "body": ["deviceId": deviceId, "token": token, "name": name],
            ]
            do {
                try self.writeJSON(hello)
                self.onLog?("ctrl: hello sent")
            } catch {
                self.onLog?("ctrl: hello failed \(error.localizedDescription)")
                self.teardownStreams()
                return
            }

            // RunLoop Timer（同队列），避免 GCD timer 被 readLoop 饿死
            let timer = Timer(timeInterval: 0.5, repeats: true) { [weak self] _ in
                guard let self, self.running else { return }
                try? self.writeJSON(["type": "heartbeat"])
            }
            RunLoop.current.add(timer, forMode: .common)
            self.hbTimer = timer
            self.readLoop()
        }
        _ = boot.wait(timeout: .now() + 10)
        if let bootErr {
            onLog?("ctrl: tcp \(bootErr)")
            disconnect()
        } else if !running {
            onLog?("ctrl: tcp connect timeout/fail")
            disconnect()
        }
    }

    func disconnect() {
        running = false
        queue.async { [weak self] in
            self?.hbTimer?.invalidate()
            self?.hbTimer = nil
            self?.teardownStreams()
        }
    }

    private func teardownStreams() {
        running = false
        input?.close()
        output?.close()
        input = nil
        output = nil
    }

    private func readLoop() {
        guard let input else { return }
        while running {
            do {
                let frame = try readFrame(input)
                handle(frame)
            } catch {
                if running { onLog?("ctrl: read end \(error.localizedDescription)") }
                teardownStreams()
                return
            }
        }
    }

    private func handle(_ data: Data) {
        guard let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = obj["type"] as? String else { return }
        let seq = (obj["seq"] as? NSNumber)?.uint64Value ?? 0
        switch type {
        case "hello_ok":
            onLog?("ctrl: hello_ok")
        case "set_quality":
            let body = obj["body"] as? [String: Any] ?? [:]
            let mode = body["mode"] as? String ?? "preview"
            let force = body["forceIdr"] as? Bool ?? true
            let w = (body["width"] as? NSNumber)?.intValue ?? 1280
            let h = (body["height"] as? NSNumber)?.intValue ?? 720
            let br = (body["bitrateKbps"] as? NSNumber)?.intValue ?? 600
            let fps = (body["fps"] as? NSNumber)?.intValue ?? 15
            onSetQuality?(mode, seq, force, w, h, br, fps)
            let ack: [String: Any] = [
                "type": "switch_ack",
                "seq": seq,
                "body": ["ok": true, "width": w, "height": h],
            ]
            try? writeJSON(ack)
            try? writeJSON(["type": "ack", "seq": seq])
        case "error":
            onLog?("ctrl error: \(obj)")
        default:
            break
        }
    }

    private func writeJSON(_ obj: [String: Any]) throws {
        guard let output, running else { throw NSError(domain: "ctrl", code: 1) }
        let payload = try JSONSerialization.data(withJSONObject: obj)
        var len = UInt32(payload.count).bigEndian
        var hdr = Data(bytes: &len, count: 4)
        hdr.append(payload)
        try writeAll(output, hdr)
    }

    private func readFrame(_ input: InputStream) throws -> Data {
        var hdr = [UInt8](repeating: 0, count: 4)
        try readExact(input, &hdr, 4)
        let n = Int(hdr[0]) << 24 | Int(hdr[1]) << 16 | Int(hdr[2]) << 8 | Int(hdr[3])
        if n <= 0 || n > 65536 { throw NSError(domain: "ctrl", code: 2) }
        var buf = [UInt8](repeating: 0, count: n)
        try readExact(input, &buf, n)
        return Data(buf)
    }

    private func readExact(_ input: InputStream, _ buf: inout [UInt8], _ count: Int) throws {
        var off = 0
        let deadline = Date().addingTimeInterval(8)
        while off < count {
            if !running { throw NSError(domain: "ctrl", code: 6) }
            if Date() > deadline {
                throw NSError(domain: "ctrl", code: 5, userInfo: [NSLocalizedDescriptionKey: "read timeout"])
            }
            // 泵 RunLoop：心跳 Timer + CFStream 回调
            RunLoop.current.run(mode: .common, before: Date().addingTimeInterval(0.05))
            guard input.hasBytesAvailable || input.streamStatus == .atEnd || input.streamStatus == .error else {
                continue
            }
            if input.streamStatus == .error || input.streamStatus == .atEnd {
                throw NSError(domain: "ctrl", code: 3)
            }
            let n = buf.withUnsafeMutableBytes { raw in
                input.read(raw.bindMemory(to: UInt8.self).baseAddress!.advanced(by: off), maxLength: count - off)
            }
            if n < 0 { throw NSError(domain: "ctrl", code: 3) }
            if n == 0 { continue }
            off += n
        }
    }

    private func writeAll(_ stream: OutputStream, _ data: Data) throws {
        var offset = 0
        while offset < data.count {
            if !stream.hasSpaceAvailable {
                RunLoop.current.run(mode: .common, before: Date().addingTimeInterval(0.02))
            }
            let wrote = data.withUnsafeBytes { raw -> Int in
                stream.write(raw.bindMemory(to: UInt8.self).baseAddress!.advanced(by: offset), maxLength: data.count - offset)
            }
            if wrote <= 0 { throw NSError(domain: "ctrl", code: 4) }
            offset += wrote
        }
    }

    private static func waitOpen(_ input: InputStream, _ output: OutputStream, timeoutMs: Int) throws {
        let deadline = Date().addingTimeInterval(Double(timeoutMs) / 1000)
        while Date() < deadline {
            let inSt = input.streamStatus
            let outSt = output.streamStatus
            if inSt == .error || outSt == .error {
                throw NSError(domain: "ctrl", code: 7, userInfo: [NSLocalizedDescriptionKey: "connect failed"])
            }
            if (inSt == .open || inSt == .reading) && (outSt == .open || outSt == .writing) { return }
            RunLoop.current.run(mode: .common, before: Date().addingTimeInterval(0.05))
        }
        throw NSError(domain: "ctrl", code: 8, userInfo: [NSLocalizedDescriptionKey: "connect timeout"])
    }
}
