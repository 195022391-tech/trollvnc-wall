import Foundation

/// 墙控制通道：[u32 BE len][json]
final class CtrlClient {
    private var input: InputStream?
    private var output: OutputStream?
    private let queue = DispatchQueue(label: "cn.local.wallphone.ctrl")
    private var running = false
    private var hbTimer: DispatchSourceTimer?
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
        input.schedule(in: .current, forMode: .common)
        output.schedule(in: .current, forMode: .common)
        input.open()
        output.open()
        let deadline = Date().addingTimeInterval(5)
        while Date() < deadline {
            let inSt = input.streamStatus
            let outSt = output.streamStatus
            if inSt == .error || outSt == .error {
                onLog?("ctrl: tcp connect failed")
                disconnect()
                return
            }
            if (inSt == .open || inSt == .reading) && (outSt == .open || outSt == .writing) { break }
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        guard input.streamStatus != .notOpen, output.streamStatus != .notOpen else {
            onLog?("ctrl: tcp connect timeout")
            disconnect()
            return
        }
        running = true

        let hello: [String: Any] = [
            "type": "hello",
            "body": ["deviceId": deviceId, "token": token, "name": name],
        ]
        do {
            try writeJSON(hello)
            onLog?("ctrl: hello sent")
        } catch {
            onLog?("ctrl: hello failed \(error.localizedDescription)")
            disconnect()
            return
        }

        queue.async { [weak self] in self?.readLoop() }
        startHeartbeat()
    }

    func disconnect() {
        running = false
        hbTimer?.cancel()
        hbTimer = nil
        input?.close()
        output?.close()
        input = nil
        output = nil
    }

    private func startHeartbeat() {
        hbTimer?.cancel()
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now() + 0.5, repeating: 0.5)
        t.setEventHandler { [weak self] in
            guard let self, self.running else { return }
            try? self.writeJSON(["type": "heartbeat"])
        }
        t.resume()
        hbTimer = t
    }

    private func readLoop() {
        guard let input else { return }
        while running {
            do {
                let frame = try readFrame(input)
                handle(frame)
            } catch {
                if running { onLog?("ctrl: read end \(error.localizedDescription)") }
                disconnect()
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
            let w = body["width"] as? Int ?? 1280
            let h = body["height"] as? Int ?? 720
            let br = body["bitrateKbps"] as? Int ?? 600
            let fps = body["fps"] as? Int ?? 15
            onSetQuality?(mode, seq, force, w, h, br, fps)
            // switch_ack
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
        guard let output else { throw NSError(domain: "ctrl", code: 1) }
        let payload = try JSONSerialization.data(withJSONObject: obj)
        var len = UInt32(payload.count).bigEndian
        var hdr = Data(bytes: &len, count: 4)
        hdr.append(payload)
        try writeAll(output, hdr)
    }

    private func readFrame(_ input: InputStream) throws -> Data {
        var hdr = [UInt8](repeating: 0, count: 4)
        try readExact(input, &hdr, 4)
        let n = (UInt32(hdr[0]) << 24) | (UInt32(hdr[1]) << 16) | (UInt32(hdr[2]) << 8) | UInt32(hdr[3])
        if n == 0 || n > 65536 { throw NSError(domain: "ctrl", code: 2) }
        var buf = [UInt8](repeating: 0, count: Int(n))
        try readExact(input, &buf, Int(n))
        return Data(buf)
    }

    private func readExact(_ input: InputStream, _ buf: inout [UInt8], _ count: Int) throws {
        var off = 0
        while off < count {
            let n = buf.withUnsafeMutableBytes { raw in
                input.read(raw.bindMemory(to: UInt8.self).baseAddress!.advanced(by: off), maxLength: count - off)
            }
            if n <= 0 { throw NSError(domain: "ctrl", code: 3) }
            off += n
        }
    }

    private func writeAll(_ stream: OutputStream, _ data: Data) throws {
        var offset = 0
        while offset < data.count {
            let wrote = data.withUnsafeBytes { raw -> Int in
                stream.write(raw.bindMemory(to: UInt8.self).baseAddress!.advanced(by: offset), maxLength: data.count - offset)
            }
            if wrote <= 0 { throw NSError(domain: "ctrl", code: 4) }
            offset += wrote
        }
    }
}
