import Foundation
import Network

/// 墙控制通道：[u32 BE len][json]
final class CtrlClient {
    private var conn: NWConnection?
    private let queue = DispatchQueue(label: "cn.local.wallphone.ctrl")
    private var running = false
    private var hbTimer: DispatchSourceTimer?
    private var recvBuf = Data()
    var onLog: ((String) -> Void)?
    var onSetQuality: ((_ mode: String, _ seq: UInt64, _ forceIdr: Bool, _ w: Int, _ h: Int, _ br: Int, _ fps: Int) -> Void)?

    func connect(host: String, port: Int, deviceId: String, token: String, name: String) {
        disconnect()
        guard let nwPort = NWEndpoint.Port(rawValue: UInt16(port)) else {
            onLog?("ctrl: bad port")
            return
        }
        let c = NWConnection(host: NWEndpoint.Host(host), port: nwPort, using: .tcp)
        conn = c
        running = true
        let ready = DispatchSemaphore(value: 0)
        var readyOK = false
        c.stateUpdateHandler = { [weak self] state in
            switch state {
            case .ready:
                readyOK = true
                ready.signal()
            case .failed(let err):
                self?.onLog?("ctrl: failed \(err)")
                ready.signal()
                self?.disconnect()
            case .cancelled:
                ready.signal()
            default:
                break
            }
        }
        c.start(queue: queue)
        _ = ready.wait(timeout: .now() + 5)
        guard running, readyOK else {
            onLog?("ctrl: tcp connect timeout/fail")
            disconnect()
            return
        }
        onLog?("ctrl: tcp ready")

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
        startReceive()
        startHeartbeat()
    }

    func disconnect() {
        running = false
        hbTimer?.cancel()
        hbTimer = nil
        conn?.cancel()
        conn = nil
        recvBuf.removeAll()
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

    private func startReceive() {
        guard let conn, running else { return }
        conn.receive(minimumIncompleteLength: 1, maximumLength: 65536) { [weak self] data, _, isComplete, error in
            guard let self, self.running else { return }
            if let error {
                self.onLog?("ctrl: recv \(error)")
                self.disconnect()
                return
            }
            if let data, !data.isEmpty {
                self.recvBuf.append(data)
                self.drainFrames()
            }
            if isComplete {
                self.onLog?("ctrl: closed")
                self.disconnect()
                return
            }
            self.startReceive()
        }
    }

    private func drainFrames() {
        while recvBuf.count >= 4 {
            let n = Int(recvBuf[0]) << 24 | Int(recvBuf[1]) << 16 | Int(recvBuf[2]) << 8 | Int(recvBuf[3])
            guard n > 0, n <= 65536 else {
                onLog?("ctrl: bad frame len \(n)")
                disconnect()
                return
            }
            guard recvBuf.count >= 4 + n else { return }
            let payload = recvBuf.subdata(in: 4 ..< (4 + n))
            recvBuf.removeSubrange(0 ..< (4 + n))
            handle(payload)
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
        guard let conn, running else { throw NSError(domain: "ctrl", code: 1) }
        let payload = try JSONSerialization.data(withJSONObject: obj)
        var len = UInt32(payload.count).bigEndian
        var frame = Data(bytes: &len, count: 4)
        frame.append(payload)
        let sem = DispatchSemaphore(value: 0)
        var sendErr: Error?
        conn.send(content: frame, completion: .contentProcessed { err in
            sendErr = err
            sem.signal()
        })
        _ = sem.wait(timeout: .now() + 3)
        if let sendErr { throw sendErr }
    }
}
