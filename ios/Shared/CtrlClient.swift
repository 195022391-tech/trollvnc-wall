import Foundation

/// 墙控制通道：`[u32 BE len][json]`
final class CtrlClient: NSObject {
    private var input: InputStream?
    private var output: OutputStream?
    private let queue = DispatchQueue(label: "cn.local.wallphone.ctrl")
    private var heartbeatTimer: DispatchSourceTimer?
    var onLog: ((String) -> Void)?
    var onSetQuality: ((_ mode: String, _ seq: UInt64, _ forceIdr: Bool, _ w: Int, _ h: Int, _ br: Int, _ fps: Int) -> Void)?
    private(set) var connected = false

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

        queue.async {
            let hello: [String: Any] = [
                "type": "hello",
                "body": ["deviceId": deviceId, "token": token, "name": name],
            ]
            do {
                try self.writeJSON(hello)
                let reply = try self.readJSON()
                self.onLog?("ctrl hello reply: \(reply["type"] ?? "?")")
                guard (reply["type"] as? String) == "hello_ok" else {
                    self.onLog?("ctrl: hello rejected (先在墙页登记 deviceId+token)")
                    self.disconnect()
                    return
                }
                self.connected = true
                self.startHeartbeat()
                self.readLoop()
            } catch {
                self.onLog?("ctrl error: \(error.localizedDescription)")
                self.disconnect()
            }
        }
    }

    func disconnect() {
        heartbeatTimer?.cancel()
        heartbeatTimer = nil
        connected = false
        input?.close()
        output?.close()
        input = nil
        output = nil
    }

    private func startHeartbeat() {
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now() + 0.5, repeating: 0.5)
        t.setEventHandler { [weak self] in
            try? self?.writeJSON(["type": "heartbeat"])
        }
        t.resume()
        heartbeatTimer = t
    }

    private func readLoop() {
        while connected {
            do {
                let msg = try readJSON()
                handle(msg)
            } catch {
                onLog?("ctrl read end: \(error.localizedDescription)")
                disconnect()
                return
            }
        }
    }

    private func handle(_ msg: [String: Any]) {
        let type = msg["type"] as? String ?? ""
        let seq = (msg["seq"] as? NSNumber)?.uint64Value ?? 0
        switch type {
        case "set_quality":
            let body = msg["body"] as? [String: Any] ?? [:]
            let mode = body["mode"] as? String ?? "preview"
            let force = body["forceIdr"] as? Bool ?? true
            let w = body["width"] as? Int ?? 1280
            let h = body["height"] as? Int ?? 720
            let br = body["bitrateKbps"] as? Int ?? 600
            let fps = body["fps"] as? Int ?? 15
            onSetQuality?(mode, seq, force, w, h, br, fps)
            // ACK + switch_ack
            try? writeJSON(["type": "ack", "seq": seq])
            let ackBody: [String: Any] = ["ok": true, "width": w, "height": h]
            try? writeJSON(["type": "switch_ack", "seq": seq, "body": ackBody])
        default:
            break
        }
    }

    private func writeJSON(_ obj: [String: Any]) throws {
        guard let output else { throw NSError(domain: "ctrl", code: 1) }
        let data = try JSONSerialization.data(withJSONObject: obj)
        var len = UInt32(data.count).bigEndian
        let hdr = Data(bytes: &len, count: 4)
        try writeAll(output, hdr)
        try writeAll(output, data)
    }

    private func readJSON() throws -> [String: Any] {
        guard let input else { throw NSError(domain: "ctrl", code: 2) }
        let hdr = try readExact(input, 4)
        let n = hdr.withUnsafeBytes { $0.load(as: UInt32.self).bigEndian }
        guard n > 0, n < 65536 else { throw NSError(domain: "ctrl", code: 3) }
        let body = try readExact(input, Int(n))
        let obj = try JSONSerialization.jsonObject(with: body)
        return obj as? [String: Any] ?? [:]
    }

    private func writeAll(_ stream: OutputStream, _ data: Data) throws {
        var offset = 0
        while offset < data.count {
            let wrote = data.withUnsafeBytes { raw -> Int in
                let ptr = raw.bindMemory(to: UInt8.self).baseAddress!.advanced(by: offset)
                return stream.write(ptr, maxLength: data.count - offset)
            }
            if wrote <= 0 { throw NSError(domain: "ctrl", code: 4) }
            offset += wrote
        }
    }

    private func readExact(_ stream: InputStream, _ n: Int) throws -> Data {
        var buf = [UInt8](repeating: 0, count: n)
        var off = 0
        while off < n {
            let r = stream.read(&buf[off], maxLength: n - off)
            if r <= 0 { throw NSError(domain: "ctrl", code: 5) }
            off += r
        }
        return Data(buf)
    }
}
